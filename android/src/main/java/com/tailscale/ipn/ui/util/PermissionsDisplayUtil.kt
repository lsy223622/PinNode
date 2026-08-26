// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

package com.tailscale.ipn.ui.util

import androidx.core.net.toUri

/** Converts a SAF URI string to a more human-friendly folder display name. */
fun friendlyDirName(uriStr: String): String {
  val uri = uriStr.toUri()
  val segment = uri.lastPathSegment ?: return uriStr

  return when {
    segment.startsWith("primary:") -> "Internal storage › " + segment.removePrefix("primary:")
    segment.contains(":") -> {
      val folder = segment.substringAfter(":")
      "SD card › $folder"
    }
    else -> segment
  }
}
