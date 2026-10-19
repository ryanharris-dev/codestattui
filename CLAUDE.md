# codestattui

## Rebuild after every request

After finishing any request that changes Go code, rebuild and reinstall the binary before reporting back:

```sh
go build -o codestattui . && cp codestattui ~/.local/bin/codestattui
```

`go build ./...` only type-checks; it does not update the binary the user runs. Tell the user to restart the TUI to pick up the change.
