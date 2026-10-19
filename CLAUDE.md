# codestattui

## Rebuild after every request

After finishing any request that changes Go code, rebuild and reinstall the binary before reporting back:

```sh
go build -o codestattui . && rm -f ~/.local/bin/codestattui && cp codestattui ~/.local/bin/codestattui && codesign --force --sign - ~/.local/bin/codestattui
```

`go build ./...` only type-checks; it does not update the binary the user runs. The `rm` and `codesign` matter on macOS: overwriting the installed binary in place with `cp` leaves a stale cached signature, and the kernel then kills it (`zsh: killed`).

Tell the user to restart the TUI to pick up the change.
