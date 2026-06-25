#!/usr/bin/env python3
"""
BOM detector / remover.

Features:
- Detect UTF-8 BOM anywhere in a file (start or middle)
- Detect UTF-16 LE / BE BOM as well
- Optionally strip the leading BOM in-place
- Keep a .bak backup when modifying files

Usage:
  python tools/bom_tool.py <file_or_glob> [<file_or_glob> ...]
  python tools/bom_tool.py go.mod
  python tools/bom_tool.py "internal/**/*.go"
  python tools/bom_tool.py --fix go.mod
  python tools/bom_tool.py --fix --no-backup "internal/**/*.go"

Known BOM signatures:
  UTF-8       : EF BB BF
  UTF-16 LE   : FF FE
  UTF-16 BE   : FE FF
  UTF-32 LE   : FF FE 00 00
  UTF-32 BE   : 00 00 FE FF
"""

from __future__ import annotations

import argparse
import glob
import os
import sys
from dataclasses import dataclass
from typing import List, Optional, Tuple


BOMS: List[Tuple[str, bytes]] = [
    ("utf-32-le", b"\xff\xfe\x00\x00"),
    ("utf-32-be", b"\x00\x00\xfe\xff"),
    ("utf-8",     b"\xef\xbb\xbf"),
    ("utf-16-le", b"\xff\xfe"),
    ("utf-16-be", b"\xfe\xff"),
]


@dataclass
class FileReport:
    path: str
    leading_bom: Optional[str]
    middle_offsets: List[Tuple[int, str]]
    modified: bool = False


def find_boms(data: bytes) -> Tuple[Optional[str], List[Tuple[int, str]]]:
    leading: Optional[str] = None
    middle: List[Tuple[int, str]] = []

    for name, sig in BOMS:
        if data.startswith(sig):
            leading = name
            break

    # Scan for BOMs beyond the first byte.
    # We check every offset, which is fine for normal-sized source files.
    for i in range(1, len(data)):
        for name, sig in BOMS:
            if data[i:i + len(sig)] == sig:
                middle.append((i, name))
                # Skip past this signature to avoid overlapping detections.
                break

    return leading, middle


def scan_file(path: str) -> FileReport:
    with open(path, "rb") as f:
        data = f.read()
    leading, middle = find_boms(data)
    return FileReport(path=path, leading_bom=leading, middle_offsets=middle)


def strip_leading_bom(data: bytes) -> Tuple[bytes, Optional[str]]:
    for name, sig in BOMS:
        if data.startswith(sig):
            return data[len(sig):], name
    return data, None


def fix_file(path: str, keep_backup: bool = True) -> Optional[FileReport]:
    with open(path, "rb") as f:
        data = f.read()

    leading, middle = find_boms(data)
    if leading is None:
        return FileReport(path=path, leading_bom=None, middle_offsets=middle, modified=False)

    cleaned, _ = strip_leading_bom(data)
    if keep_backup:
        backup = path + ".bak"
        if not os.path.exists(backup):
            with open(backup, "wb") as bf:
                bf.write(data)

    with open(path, "wb") as f:
        f.write(cleaned)

    return FileReport(path=path, leading_bom=leading, middle_offsets=middle, modified=True)


def expand_patterns(patterns: List[str]) -> List[str]:
    files: List[str] = []
    for pat in patterns:
        matched = glob.glob(pat, recursive=True)
        if matched:
            files.extend(matched)
        elif os.path.isfile(pat):
            files.append(pat)
    # De-duplicate while preserving order.
    seen = set()
    result: List[str] = []
    for p in files:
        rp = os.path.realpath(p)
        if rp not in seen:
            seen.add(rp)
            result.append(p)
    return result


def print_report(report: FileReport) -> None:
    print(f"File: {report.path}")
    if report.leading_bom:
        print(f"  Leading BOM : {report.leading_bom}")
    else:
        print("  Leading BOM : none")

    if report.middle_offsets:
        print(f"  Middle BOMs : {len(report.middle_offsets)} found")
        for offset, name in report.middle_offsets[:20]:
            print(f"    - {name} at byte offset {offset}")
        if len(report.middle_offsets) > 20:
            print(f"    ... and {len(report.middle_offsets) - 20} more")
    else:
        print("  Middle BOMs : none")

    if report.modified:
        print("  Action      : leading BOM removed in-place")


def main() -> int:
    parser = argparse.ArgumentParser(description="Detect and remove BOM characters in files")
    parser.add_argument("paths", nargs="+", help="Files or glob patterns to inspect")
    parser.add_argument("--fix", action="store_true", help="Remove leading BOM in-place")
    parser.add_argument("--no-backup", action="store_true", help="Do not create .bak files when fixing")
    args = parser.parse_args()

    targets = expand_patterns(args.paths)
    if not targets:
        print("No files matched.")
        return 1

    had_middle = False
    had_leading = False
    fixed = 0

    for path in targets:
        if args.fix:
            report = fix_file(path, keep_backup=not args.no_backup)
            if report is None:
                continue
            if report.modified:
                fixed += 1
        else:
            report = scan_file(path)

        print_report(report)
        print("")

        if report.leading_bom:
            had_leading = True
        if report.middle_offsets:
            had_middle = True

    print("-" * 50)
    print(f"Scanned : {len(targets)} file(s)")
    if args.fix:
        print(f"Fixed   : {fixed} file(s)")

    if had_middle:
        print("Warning : middle BOM detected in at least one file")
        return 2

    if had_leading and not args.fix:
        return 1

    return 0


if __name__ == "__main__":
    sys.exit(main())
