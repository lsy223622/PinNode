# PinNode branding assets

`pinnode-nine-ring.svg` is the canonical PinNode mark. The Android launcher,
notification icons, Android tile, TV banner, in-app Compose geometry, and server
admin mark are generated from it so their centers, dot size, and opacity pattern
stay synchronized. The TV banner keeps its existing PinNode text wordmark while
its nine-dot portion is regenerated.

After editing the canonical SVG, regenerate the tracked consumers from the
repository root:

```sh
python3 scripts/sync_pinnode_logo.py --write
```

On Windows, use `python` if that is the installed command. Use
`python3 scripts/sync_pinnode_logo.py --check` to verify that no generated file
has drifted; CI runs this check. Generated files are not intended to be edited
manually.
