--!strict
local registry = assert(tern.plugin.data:match("^(.*)/plugin%-data/[^/]+$"), "Cannot locate Tern plugin registry") .. "/plugins"
local goDirectory = registry .. "/go-navigation"
local link = registry .. "/go-navigation.path"
if tern.fs.exists(link) then goDirectory = tern.fs.read(link, 16384):match("^%s*(.-)%s*$") end
-- The cooperative Go host loads this package's frontend in its own VM. A
-- second registration would split history and make identical keys conflict.
if tern.fs.exists(goDirectory .. "/jvm-navigation-host.luau") then return end
local factory = assert(loadstring(tern.fs.read(tern.plugin.dir .. "/navigation.luau", 65536), "@navigation.luau"))()
factory(tern.plugin.dir, nil)
