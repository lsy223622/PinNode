#!/usr/bin/env python3
"""Generate PinNode logo consumers from the canonical SVG asset."""

from __future__ import annotations

import argparse
import math
import struct
import sys
import zlib
from dataclasses import dataclass
from pathlib import Path
from xml.etree import ElementTree


ROOT = Path(__file__).resolve().parents[1]
CANONICAL = ROOT / "assets" / "pinnode-nine-ring.svg"

SERVER_MARK = ROOT / "server" / "web" / "mark.svg"
ANDROID_LAUNCHER = ROOT / "android" / "src" / "main" / "res" / "drawable" / "ic_launcher_foreground.xml"
ANDROID_TILE = ROOT / "android" / "src" / "main" / "res" / "drawable" / "ic_tile.xml"
ANDROID_NOTIFICATION = ROOT / "android" / "src" / "main" / "res" / "drawable" / "ic_notification.xml"
ANDROID_NOTIFICATION_DISABLED = (
    ROOT / "android" / "src" / "main" / "res" / "drawable" / "ic_notification_disabled.xml"
)
TV_BANNER = ROOT / "android" / "src" / "main" / "res" / "drawable-nodpi" / "tv_banner.png"
ANDROID_GEOMETRY = (
    ROOT
    / "android"
    / "src"
    / "main"
    / "java"
    / "com"
    / "tailscale"
    / "ipn"
    / "ui"
    / "view"
    / "PinNodeLogoGeometry.kt"
)

ANDROID_SCALE = 0.72
ANDROID_OFFSET = 18.0
TV_BANNER_SCALE = 5.0 / 6.0
TV_BANNER_OFFSET_X = 23.75
TV_BANNER_OFFSET_Y = 47.75
TV_BANNER_LOGO_BOUNDS = (39, 62, 94, 118)


@dataclass(frozen=True)
class Ring:
    x: float
    y: float
    radius: float
    opacity: float


@dataclass(frozen=True)
class Logo:
    source: bytes
    rings: tuple[Ring, ...]


def local_name(tag: str) -> str:
    return tag.rsplit("}", 1)[-1]


def parse_float(value: str, attribute: str) -> float:
    try:
        return float(value)
    except ValueError as exc:
        raise ValueError(f"{attribute} must be numeric, got {value!r}") from exc


def parse_logo() -> Logo:
    source = CANONICAL.read_bytes()
    root = ElementTree.fromstring(source)
    view_box = root.attrib.get("viewBox", "").replace(",", " ").split()
    if len(view_box) != 4 or any(
        not math.isclose(parse_float(value, "viewBox"), expected)
        for value, expected in zip(view_box, (0.0, 0.0, 100.0, 100.0))
    ):
        raise ValueError("canonical SVG must use viewBox=\"0 0 100 100\"")

    groups = [element for element in root.iter() if local_name(element.tag) == "g"]
    if len(groups) != 1 or groups[0].attrib.get("fill", "").lower() in {"", "none"}:
        raise ValueError("canonical SVG must contain one filled group")

    circles = [element for element in root.iter() if local_name(element.tag) == "circle"]
    if len(circles) != 9:
        raise ValueError(f"canonical SVG must contain exactly 9 circles, got {len(circles)}")

    rings: list[Ring] = []
    for circle in circles:
        try:
            x = parse_float(circle.attrib["cx"], "cx")
            y = parse_float(circle.attrib["cy"], "cy")
            radius = parse_float(circle.attrib["r"], "r")
        except KeyError as exc:
            raise ValueError(f"circle is missing {exc.args[0]}") from exc
        opacity = parse_float(circle.attrib.get("fill-opacity", "1"), "fill-opacity")
        if not 0.0 <= opacity <= 1.0:
            raise ValueError("fill-opacity must be between 0 and 1")
        rings.append(Ring(x=x, y=y, radius=radius, opacity=opacity))

    radii = {ring.radius for ring in rings}
    if len(radii) != 1 or next(iter(radii)) <= 0:
        raise ValueError("all circles must use one positive radius")
    x_positions = sorted({ring.x for ring in rings})
    y_positions = sorted({ring.y for ring in rings})
    if len(x_positions) != 3 or len(y_positions) != 3:
        raise ValueError("canonical SVG must use three x and three y positions")
    if {(ring.x, ring.y) for ring in rings} != {
        (x, y) for y in y_positions for x in x_positions
    }:
        raise ValueError("canonical SVG must contain every position in the 3x3 grid")

    return Logo(source=source, rings=tuple(rings))


def number(value: float, digits: int = 6) -> str:
    text = f"{value:.{digits}f}".rstrip("0").rstrip(".")
    return "0" if text in {"", "-0"} else text


def kotlin_number(value: float) -> str:
    return f"{number(value, 8)}f"


def matrix(logo: Logo) -> list[list[bool]]:
    x_positions = sorted({ring.x for ring in logo.rings})
    y_positions = sorted({ring.y for ring in logo.rings})
    rings = {(ring.x, ring.y): ring for ring in logo.rings}
    return [
        [rings[(x, y)].opacity > 0.5 for x in x_positions]
        for y in y_positions
    ]


def android_point(value: float) -> float:
    return value * ANDROID_SCALE + ANDROID_OFFSET


def android_path(ring: Ring) -> str:
    x = android_point(ring.x)
    y = android_point(ring.y)
    radius = ring.radius * ANDROID_SCALE
    diameter = radius * 2
    return (
        f"M{number(x + radius, 3)},{number(y, 3)}"
        f"a{number(radius, 3)},{number(radius, 3)} 0,1 0,-{number(diameter, 3)} 0"
        f"a{number(radius, 3)},{number(radius, 3)} 0,1 0,{number(diameter, 3)} 0z"
    )


def launcher_xml(logo: Logo) -> bytes:
    lines = [
        "<!-- Copyright (c) PinNode contributors",
        "     SPDX-License-Identifier: BSD-3-Clause -->",
        "<!-- GENERATED FILE: source assets/pinnode-nine-ring.svg. -->",
        '<vector xmlns:android="http://schemas.android.com/apk/res/android"',
        '    android:width="108dp"',
        '    android:height="108dp"',
        '    android:viewportWidth="108"',
        '    android:viewportHeight="108">',
    ]
    for ring in logo.rings:
        lines.extend(
            [
                "  <path",
                f'      android:pathData="{android_path(ring)}"',
                '      android:fillColor="#ffffff"',
            ]
        )
        if ring.opacity < 1.0:
            lines.append(f'      android:fillAlpha="{number(ring.opacity)}"')
        lines.append("      />")
    lines.append("</vector>")
    return ("\n".join(lines) + "\n").encode("utf-8")


def tile_xml(logo: Logo) -> bytes:
    radius = next(iter({ring.radius for ring in logo.rings}))
    lines = [
        '<vector xmlns:android="http://schemas.android.com/apk/res/android"',
        '    android:width="24dp"',
        '    android:height="24dp"',
        '    android:viewportWidth="100"',
        '    android:viewportHeight="100">',
        "  <!-- GENERATED FILE: source assets/pinnode-nine-ring.svg. -->",
    ]
    for ring in logo.rings:
        diameter = radius * 2
        path = (
            f"M{number(ring.x + radius)},{number(ring.y)}"
            f"a{number(radius)},{number(radius)} 0,1 0,-{number(diameter)} 0"
            f"a{number(radius)},{number(radius)} 0,1 0,{number(diameter)} 0z"
        )
        color = "#FFFDFA" if ring.opacity > 0.5 else "#54514D"
        lines.append(
            f'  <path android:pathData="{path}" android:fillColor="{color}"/>'
        )
    lines.append("</vector>")
    return ("\n".join(lines) + "\n").encode("utf-8")


def notification_xml(logo: Logo, disabled: bool) -> bytes:
    lines = [
        '<vector xmlns:android="http://schemas.android.com/apk/res/android"',
        '    android:width="100dp"',
        '    android:height="100dp"',
        '    android:viewportWidth="100"',
        '    android:viewportHeight="100">',
        "  <!-- GENERATED FILE: source assets/pinnode-nine-ring.svg. -->",
    ]
    radius = next(iter({ring.radius for ring in logo.rings}))
    diameter = radius * 2
    for ring in logo.rings:
        path = (
            f"M{number(ring.x + radius)},{number(ring.y)}"
            f"a{number(radius)},{number(radius)} 0,1 0,-{number(diameter)} 0"
            f"a{number(radius)},{number(radius)} 0,1 0,{number(diameter)} 0z"
        )
        opacity = 0.4 if disabled else ring.opacity
        alpha = f' android:fillAlpha="{number(opacity)}"' if opacity < 1.0 else ""
        lines.append(
            f'  <path android:pathData="{path}" android:fillColor="#000000"{alpha}/>'
        )
    lines.append("</vector>")
    return ("\n".join(lines) + "\n").encode("utf-8")


# The TV banner is a composite raster (the existing PinNode wordmark is kept),
# so use a tiny stdlib-only PNG reader/writer instead of adding an image dependency.
def paeth(a: int, b: int, c: int) -> int:
    estimate = a + b - c
    pa = abs(estimate - a)
    pb = abs(estimate - b)
    pc = abs(estimate - c)
    if pa <= pb and pa <= pc:
        return a
    if pb <= pc:
        return b
    return c


def read_banner_png() -> tuple[int, int, bytearray, bytes]:
    source = TV_BANNER.read_bytes()
    if source[:8] != b"\x89PNG\r\n\x1a\n":
        raise ValueError("TV banner is not a PNG")
    offset = 8
    width = height = None
    idat = bytearray()
    while offset < len(source):
        length = struct.unpack(">I", source[offset : offset + 4])[0]
        kind = source[offset + 4 : offset + 8]
        payload = source[offset + 8 : offset + 8 + length]
        offset += 12 + length
        if kind == b"IHDR":
            width, height, depth, color_type, compression, filter_method, interlace = struct.unpack(
                ">IIBBBBB", payload
            )
            if (depth, color_type, compression, filter_method, interlace) != (8, 6, 0, 0, 0):
                raise ValueError("TV banner must be a non-interlaced 8-bit RGBA PNG")
        elif kind == b"IDAT":
            idat.extend(payload)
        elif kind == b"IEND":
            break
    if width is None or height is None:
        raise ValueError("TV banner PNG is missing IHDR")
    if (width, height) != (320, 180):
        raise ValueError("TV banner must be 320x180 pixels")

    raw = zlib.decompress(idat)
    stride = width * 4
    expected = height * (stride + 1)
    if len(raw) != expected:
        raise ValueError("TV banner PNG has an unexpected scanline length")
    pixels = bytearray(height * stride)
    previous = bytearray(stride)
    cursor = 0
    for y in range(height):
        filter_type = raw[cursor]
        cursor += 1
        row = bytearray(raw[cursor : cursor + stride])
        cursor += stride
        for x in range(stride):
            left = row[x - 4] if x >= 4 else 0
            upper = previous[x]
            upper_left = previous[x - 4] if x >= 4 else 0
            if filter_type == 1:
                row[x] = (row[x] + left) & 0xFF
            elif filter_type == 2:
                row[x] = (row[x] + upper) & 0xFF
            elif filter_type == 3:
                row[x] = (row[x] + ((left + upper) // 2)) & 0xFF
            elif filter_type == 4:
                row[x] = (row[x] + paeth(left, upper, upper_left)) & 0xFF
            elif filter_type != 0:
                raise ValueError(f"TV banner uses unsupported PNG filter {filter_type}")
        start = y * stride
        pixels[start : start + stride] = row
        previous = row
    return width, height, pixels, source


def png_chunk(kind: bytes, payload: bytes) -> bytes:
    return (
        struct.pack(">I", len(payload))
        + kind
        + payload
        + struct.pack(">I", zlib.crc32(kind + payload) & 0xFFFFFFFF)
    )


def write_banner_png(width: int, height: int, pixels: bytearray) -> bytes:
    stride = width * 4
    raw = bytearray()
    for y in range(height):
        raw.append(0)
        raw.extend(pixels[y * stride : (y + 1) * stride])
    ihdr = struct.pack(">IIBBBBB", width, height, 8, 6, 0, 0, 0)
    return (
        b"\x89PNG\r\n\x1a\n"
        + png_chunk(b"IHDR", ihdr)
        + png_chunk(b"IDAT", zlib.compress(bytes(raw), level=9))
        + png_chunk(b"IEND", b"")
    )


def render_tv_banner(logo: Logo) -> tuple[int, int, bytearray]:
    width, height, pixels, _ = read_banner_png()
    min_x, min_y, max_x, max_y = TV_BANNER_LOGO_BOUNDS
    background = (31, 30, 30, 255)
    for y in range(min_y, max_y):
        for x in range(min_x, max_x):
            index = (y * width + x) * 4
            pixels[index : index + 4] = bytes(background)

    radius = next(iter({ring.radius for ring in logo.rings})) * TV_BANNER_SCALE
    sample_count = 8
    for ring in logo.rings:
        center_x = ring.x * TV_BANNER_SCALE + TV_BANNER_OFFSET_X
        center_y = ring.y * TV_BANNER_SCALE + TV_BANNER_OFFSET_Y
        outer = radius
        x_start = max(min_x, math.floor(center_x - outer - 1))
        x_end = min(max_x, math.ceil(center_x + outer + 1))
        y_start = max(min_y, math.floor(center_y - outer - 1))
        y_end = min(max_y, math.ceil(center_y + outer + 1))
        for y in range(y_start, y_end):
            for x in range(x_start, x_end):
                covered = 0
                for sample_y in range(sample_count):
                    py = y + (sample_y + 0.5) / sample_count
                    for sample_x in range(sample_count):
                        px = x + (sample_x + 0.5) / sample_count
                        distance = math.hypot(px - center_x, py - center_y)
                        if distance <= outer:
                            covered += 1
                if not covered:
                    continue
                alpha = covered / (sample_count * sample_count) * ring.opacity
                index = (y * width + x) * 4
                for channel in range(3):
                    pixels[index + channel] = round(
                        pixels[index + channel] * (1.0 - alpha) + 255.0 * alpha
                    )
                pixels[index + 3] = 255
    return width, height, pixels


def tv_banner_png(logo: Logo) -> bytes:
    width, height, pixels = render_tv_banner(logo)
    return write_banner_png(width, height, pixels)


def tv_banner_matches(logo: Logo) -> bool:
    width, height, pixels, _ = read_banner_png()
    expected_width, expected_height, expected_pixels = render_tv_banner(logo)
    return (width, height, pixels) == (expected_width, expected_height, expected_pixels)


def geometry_kt(logo: Logo) -> bytes:
    x_positions = sorted({ring.x for ring in logo.rings})
    y_positions = sorted({ring.y for ring in logo.rings})
    radius = next(iter({ring.radius for ring in logo.rings}))
    dots = matrix(logo)
    lines = [
        "// Copyright (c) PinNode contributors",
        "// SPDX-License-Identifier: BSD-3-Clause",
        "",
        "// GENERATED FILE: source assets/pinnode-nine-ring.svg.",
        "package com.tailscale.ipn.ui.view",
        "",
        "internal object PinNodeLogoGeometry {",
        "  val xPositions =",
        "      floatArrayOf(",
        *[f"          {kotlin_number(x / 100.0)}," for x in x_positions],
        "      )",
        "  val yPositions =",
        "      floatArrayOf(",
        *[f"          {kotlin_number(y / 100.0)}," for y in y_positions],
        "      )",
        f"  const val dotRadius = {kotlin_number(radius / 100.0)}",
        "",
        "  val defaultDotsMatrix: List<List<Boolean>> =",
        "      listOf(",
    ]
    for row in dots:
        values = ", ".join("true" if enabled else "false" for enabled in row)
        lines.append(f"          listOf({values}),")
    lines.extend(["      )", "}"])
    return ("\n".join(lines) + "\n").encode("utf-8")


def generated_files(logo: Logo) -> dict[Path, bytes]:
    return {
        SERVER_MARK: logo.source,
        ANDROID_LAUNCHER: launcher_xml(logo),
        ANDROID_TILE: tile_xml(logo),
        ANDROID_NOTIFICATION: notification_xml(logo, disabled=False),
        ANDROID_NOTIFICATION_DISABLED: notification_xml(logo, disabled=True),
        TV_BANNER: tv_banner_png(logo),
        ANDROID_GEOMETRY: geometry_kt(logo),
    }


def write_if_changed(path: Path, content: bytes) -> bool:
    if path.exists() and path.read_bytes() == content:
        return False
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(content)
    return True


def write_consumer(path: Path, content: bytes, logo: Logo) -> bool:
    # Compare the banner's decoded pixels so zlib implementation differences do
    # not dirty a checkout when the rendered logo is already current.
    if path == TV_BANNER and path.exists() and tv_banner_matches(logo):
        return False
    return write_if_changed(path, content)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    modes = parser.add_mutually_exclusive_group(required=True)
    modes.add_argument("--write", action="store_true", help="write generated consumers")
    modes.add_argument("--check", action="store_true", help="fail if consumers are out of date")
    args = parser.parse_args()

    try:
        logo = parse_logo()
        outputs = generated_files(logo)
    except (OSError, ElementTree.ParseError, ValueError) as exc:
        print(f"PinNode logo sync failed: {exc}", file=sys.stderr)
        return 1

    if args.write:
        changed = [
            str(path.relative_to(ROOT))
            for path, content in outputs.items()
            if write_consumer(path, content, logo)
        ]
        if changed:
            print("Updated PinNode logo consumers:")
            print("\n".join(f"  {path}" for path in changed))
        else:
            print("PinNode logo consumers are already synchronized.")
        return 0

    stale = []
    for path, content in outputs.items():
        if path == TV_BANNER:
            if not path.exists() or not tv_banner_matches(logo):
                stale.append(str(path.relative_to(ROOT)))
        elif not path.exists() or path.read_bytes() != content:
            stale.append(str(path.relative_to(ROOT)))
    if stale:
        print("PinNode logo consumers are out of date:", file=sys.stderr)
        print("\n".join(f"  {path}" for path in stale), file=sys.stderr)
        print("Run: python3 scripts/sync_pinnode_logo.py --write", file=sys.stderr)
        return 1
    print("PinNode logo consumers are synchronized.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
