#!/usr/bin/env python3
"""Build the single license and attribution bundle used by release artifacts."""

from __future__ import annotations

import argparse
import sys
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
OUTPUT = ROOT / "LICENSES.md"
SECTIONS = (
    ("PinNode GPLv3 license", ROOT / "LICENSE"),
    ("PinNode notices", ROOT / "NOTICE"),
    ("Tailscale patent grant", ROOT / "PATENTS"),
    ("Third-party notices", ROOT / "THIRD_PARTY_NOTICES.md"),
    (
        "Tailscale Android dependency inventory",
        ROOT / "third_party" / "tailscale" / "licenses" / "android.md",
    ),
    (
        "Tailscale core dependency inventory",
        ROOT / "third_party" / "tailscale" / "licenses" / "tailscale.md",
    ),
)


def render_bundle() -> str:
    parts = [
        "# PinNode license bundle",
        "",
        "This file combines the license, attribution, patent and dependency "
        "materials that accompany PinNode release binaries.",
        "",
        "The individual source files remain authoritative in the repository. "
        "This aggregate preserves their text and adds the versioned Tailscale "
        "dependency inventories used by the Android and Linux builds.",
        "",
    ]
    for title, source in SECTIONS:
        if not source.is_file():
            raise FileNotFoundError(source)
        parts.extend((f"## {title}", "", source.read_text(encoding="utf-8").rstrip(), ""))
    return "\n".join(parts)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, default=OUTPUT)
    parser.add_argument("--check", action="store_true")
    args = parser.parse_args()

    try:
        expected = render_bundle()
    except (FileNotFoundError, OSError) as error:
        print(f"cannot build license bundle: {error}", file=sys.stderr)
        return 1

    output = args.output.resolve()
    if args.check:
        try:
            actual = output.read_text(encoding="utf-8")
        except OSError as error:
            print(f"cannot read {output}: {error}", file=sys.stderr)
            return 1
        if actual != expected:
            print(f"{output} is stale; run scripts/build_license_bundle.py", file=sys.stderr)
            return 1
        return 0

    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(expected, encoding="utf-8", newline="\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
