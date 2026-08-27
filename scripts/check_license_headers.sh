#!/bin/sh
#
# Copyright (c) Tailscale Inc & AUTHORS
# SPDX-License-Identifier: BSD-3-Clause
#
# check_license_headers.sh checks source files in the project tree that carry
# an SPDX copyright header. Vendored upstream sources are checked by their own
# license metadata and are intentionally outside this project-specific check.

check_file() {
	got=$1
	case "$got" in
		"// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause"|"// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause"|"// Copyright (c) PinNode contributors
// SPDX-License-Identifier: BSD-3-Clause")
			return 0
			;;
	esac
	return 1
}

if [ $# != 1 ]; then
	echo "Usage: $0 rootdir" >&2
	exit 1
fi

fail=0
files=$(find "$1" \
	\( -type d \( \
		-name .git -o -name node_modules -o -name third_party -o -name .cache \
		-o -name .gradle -o -name .tmp -o -name .upstream -o -name out \
		-o -name dist -o -name build -o -name .cxx -o -name .externalNativeBuild \
	\) -prune \) -o \
	\( -type f \( -name '*.go' -o -name '*.tsx' -o -name '*.ts' -o -name '*.kt' -o -name '*.java' \) \
		! -name '*.config.ts' -print \))

while IFS= read -r file; do
	[ -n "$file" ] || continue
	header=$(head -n 2 "$file" | tr -d '\r')
	case "$header" in
		"// Copyright "*)
			if ! check_file "$header"; then
				fail=1
				echo "${file#$1/} doesn't have the right copyright header:"
				echo "$header" | sed -e 's/^/    /g'
			fi
			;;
	esac
done <<EOF
$files
EOF

if [ $fail -ne 0 ]; then
	exit 1
fi
