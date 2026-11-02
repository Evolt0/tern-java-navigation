--!strict
-- One VM owns keys, generation guards and history even when both plugins are installed.
return function(javaDirectory: string, goDirectory: string?)
local label = if goDirectory then "Go / Java / Kotlin" else "Java / Kotlin"
local function backend(path: string): string
	if path:sub(-3) == ".go" and goDirectory then return goDirectory .. "/bin/tern-go-navigation" end
	return javaDirectory .. "/bin/tern-java-navigation"
end

local history: { any } = {}
local generation = 0
local lastSearch = ""
local warmAttempted: { [string]: boolean } = {}
local warming = false

local function fileURL(path: string, line: number, col: number): string
	local escaped = path:gsub("[^%w%-%._~/]", function(c)
		return string.format("%%%02X", string.byte(c))
	end)
	return "file://" .. escaped .. "#L" .. line .. "C" .. col
end

local function isSource(path: string): boolean
	return path:sub(-5) == ".java" or path:sub(-3) == ".kt" or path:sub(-4) == ".kts" or (goDirectory ~= nil and path:sub(-3) == ".go")
end

local function sourcePane(cx: any): number?
	local pane = cx.session:focused()
	if not pane then return nil end
	local info = cx.session:pane(pane)
	if not info or info.kind ~= "file" or not info.path or not isSource(info.path) then
		return nil
	end
	return pane
end

local function focusedSource(cx: any): any
	local pane = sourcePane(cx)
	return if pane then cx.session:read(pane, { lines = 1, max_chars = 1 }) else nil
end

local function unchangedPosition(cx: any, origin: any): boolean
	if cx.session:focused() ~= origin.pane then return false end
	local current = cx.session:read(origin.pane, { lines = 1, max_chars = 1 })
	if not current or current.path ~= origin.path or not current.caret
		or current.caret.line ~= origin.line or current.caret.col ~= origin.col then
		return false
	end
	local before, after = origin.selection, current.selection
	if before == nil or after == nil then return before == after end
	return before.anchor == after.anchor and before.head == after.head
end

-- Re-read every captured overlay before navigating: edits during indexing invalidate the result.
local function readDocuments(cx: any, paths: { string }, token: number, done: any)
	local documents: { [string]: string } = {}
	local remaining = #paths
	local failed = false
	for _, path in paths do
		cx.docs:read(path):next(function(text: any, err: any, nextCx: any)
			if failed or token ~= generation then return end
			if err or text == nil then
				failed = true
				nextCx:toast("error", "Code navigation: cannot read editor buffer", tostring(err or path))
				return
			end
			documents[path] = text
			remaining -= 1
			if remaining == 0 then done(documents, nextCx) end
		end)
	end
end

local function goTo(cx: any, action: string, search: string?)
	local current = focusedSource(cx)
	if not current or not current.caret or current.load ~= "ready" then
		cx:toast("info", "Focus a loaded Code source editor first")
		return
	end
	if current.host ~= 0 then
		cx:toast("error", "Code navigation currently requires a local file", "The language server runs on this computer, not a remote host.")
		return
	end
	generation += 1
	local token = generation
	local origin = {
		pane = current.info.pane, path = current.path,
		line = current.caret.line, col = current.caret.col,
		selection = if current.selection then { anchor = current.selection.anchor, head = current.selection.head } else nil,
	}
	local paths = { origin.path }
	local seen = { [origin.path] = true }
	for _, pane in cx.session:panes() do
		if pane.kind == "file" and pane.path and isSource(pane.path) and (pane.path:sub(-3) == ".go") == (origin.path:sub(-3) == ".go") and not seen[pane.path] then
			local snapshot = cx.session:read(pane.pane, { lines = 1, max_chars = 1 })
			if snapshot and snapshot.host == 0 and snapshot.load == "ready" then
				seen[pane.path] = true
				table.insert(paths, pane.path)
			end
		end
	end
	readDocuments(cx, paths, token, function(documents: any, live: any)
		if not unchangedPosition(live, origin) then return end
		local request = tern.json.encode({
			action = action, search = search,
			path = origin.path, line = origin.line, col = origin.col, documents = documents,
			selection = origin.selection,
			tabWidth = live.settings:get("files.tab_width"),
		})
		local progress = tern.timer(600, function(progressCx)
			if token == generation then
				progressCx:toast("info", "Code: 正在查询，首次加载项目可能需要一些时间…")
			end
		end)
		tern.process.run({ backend(origin.path) }, {
			stdin = request, timeout_ms = 185000,
		}):next(function(result: any, err: any, nextCx: any)
			progress:cancel()
			if token ~= generation then return end
			if err or not result or result.status ~= 0 then
				nextCx:toast("error", "Code navigation failed", tostring(err or (result and result.stderr) or "No process result"))
				return
			end
			local ok, targets = pcall(tern.json.decode, result.stdout)
			if not ok or type(targets) ~= "table" then
				nextCx:toast("error", "Code navigation returned invalid JSON")
				return
			end
			if #targets == 0 then
				nextCx:toast("info", if action == "symbols" then "当前项目未找到匹配的符号" else "No Code definition or references at this position")
				return
			end
			local function navigate(target: any, navCx: any)
				if token ~= generation then return end
				readDocuments(navCx, paths, token, function(latest: any, checkedCx: any)
					if not unchangedPosition(checkedCx, origin) then
						checkedCx:toast("info", "Code: cursor moved; run navigation again")
						return
					end
					for path, text in documents do
						if latest[path] ~= text then
							checkedCx:toast("info", "Code: buffer changed; run navigation again")
							return
						end
					end
					checkedCx:open(target.url)
					table.insert(history, origin)
				end)
			end
			if #targets == 1 and action == "navigate" then
				navigate(targets[1], nextCx)
			else
				local items = {}
				for _, target in targets do
					local name = target.path:match("[^/]+$") or target.path
					if target.kind == "symbol" then name = target.name .. "  —  " .. name end
					table.insert(items, {
						label = name .. ":" .. target.line .. ":" .. target.col,
						detail = target.preview .. "  —  " .. target.path,
						group = if target.kind == "symbol" then "项目符号" elseif target.kind == "definition" then "定义" else "调用 / 引用",
						target = target,
					})
				end
				local placeholder = if action == "symbols" then "Code: 项目符号 · " .. search else "Code: 定义与调用 / 引用"
				nextCx:pick({ items = items, placeholder = placeholder, action = "jump" }):next(function(choice: any, pickErr: any, pickCx: any)
					if pickErr then pickCx:toast("error", "Code navigation picker failed", tostring(pickErr)) end
					if choice then navigate(choice.target, pickCx) end
				end)
			end
		end)
	end)
end

tern.command({
	id = "navigate", title = label .. ": Navigate symbol", group = label .. " Navigation",
	keys = { "ctrl+alt+g" },
	run = function(cx) goTo(cx, "navigate") end,
})

tern.command({
	id = "symbols", title = label .. ": Search project symbols", group = label .. " Navigation",
	keys = { "ctrl+alt+s" },
	run = function(cx)
		local current = focusedSource(cx)
		if not current or not current.caret or current.load ~= "ready" or current.host ~= 0 then
			cx:toast("info", "请先打开当前项目中的本地源码文件")
			return
		end
		generation += 1
		local token = generation
		local origin = { pane = current.info.pane, path = current.path, line = current.caret.line, col = current.caret.col,
			selection = if current.selection then { anchor = current.selection.anchor, head = current.selection.head } else nil }
		cx:prompt({ placeholder = "搜索当前项目的函数、类型、方法、字段", value = lastSearch, action = "search" }):next(function(value, err, nextCx)
			if token ~= generation then return end
			if err then nextCx:toast("error", "Code symbol search failed", tostring(err)); return end
			if value == nil then return end
			local search = value:match("^%s*(.-)%s*$")
			if search == "" then nextCx:toast("info", "请输入要查询的符号名称"); return end
			if not unchangedPosition(nextCx, origin) then
				nextCx:toast("info", "Code: 编辑位置已改变，请重新查询")
				return
			end
			lastSearch = search
			goTo(nextCx, "symbols", search)
		end)
	end,
})

tern.command({
	id = "back", title = label .. ": Jump back", group = label .. " Navigation",
	keys = { "ctrl+alt+b" },
	run = function(cx)
		generation += 1
		local previous = table.remove(history)
		if not previous then
			cx:toast("info", "No Code jump history")
			return
		end
		if cx.session:focused() ~= previous.pane and cx.session:pane(previous.pane) then
			cx.layout:focus(previous.pane)
		end
		cx:open(fileURL(previous.path, previous.line, previous.col))
	end,
})

-- Native Tern exposes pane focus, not document-loaded events. Check only the
-- focused pane's metadata; read its buffer once when first warming that file.
local function warmFocused(cx: any)
	if warming then return end
	local current = focusedSource(cx)
	if not current or current.load ~= "ready" or current.host ~= 0 or warmAttempted[current.path] then return end
	local path = current.path
	warming = true
	warmAttempted[path] = true
	cx.docs:read(path):next(function(text, err, nextCx)
		local focused = focusedSource(nextCx)
		if err or text == nil or not focused or focused.path ~= path then warming = false; return end
		local request = tern.json.encode({ action = "warmup", path = path,
			tabWidth = nextCx.settings:get("files.tab_width"), documents = { [path] = text } })
		tern.process.run({ backend(path) }, { stdin = request, timeout_ms = 185000 }):next(function()
			-- Explicit navigation reports errors; background warmup never retries or interrupts editing.
			warming = false
		end)
	end)
end

local function warmTick(cx: any)
	warmFocused(cx)
	tern.timer(1000, warmTick)
end

tern.timer(250, warmTick)

end
