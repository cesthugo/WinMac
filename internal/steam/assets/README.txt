cefwrapper.exe is generated here by `go generate ./internal/steam` (it is not
committed). It is built from tools/cefwrapper for windows/amd64 and embedded
into cave, which installs it in place of Steam's steamwebhelper.exe.
