// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause
package com.tailscale.ipn.util

import android.content.Context
import android.util.Log
import java.time.Instant
import libtailscale.Libtailscale

object TSLog {
  data class PinNodeLogEntry(
      val timestamp: String,
      val level: String,
      val component: String,
      val message: String,
  )

  private lateinit var appContext: Context
  var libtailscaleWrapper = LibtailscaleWrapper()
  @Volatile var pinNodeLogSink: ((PinNodeLogEntry) -> Unit)? = null

  fun init(context: Context) {
    appContext = context.applicationContext
  }

  fun d(tag: String?, message: String) {
    Log.d(tag, message)
    emitRemote("DEBUG", tag, message)
  }

  fun w(tag: String, message: String) {
    Log.w(tag, message)
    emitRemote("WARN", tag, message)
  }

  fun v(tag: String?, message: String) {
    if (isUnstableRelease()) {
      Log.v(tag, message)
      emitRemote("DEBUG", tag, message)
    }
  }

  // Overloaded function without Throwable because Java does not support default parameters
  @JvmStatic
  fun e(tag: String?, message: String) {
    Log.e(tag, message)
    emitRemote("ERROR", tag, message)
  }

  fun e(tag: String?, message: String, throwable: Throwable? = null) {
    if (throwable == null) {
      Log.e(tag, message)
      emitRemote("ERROR", tag, message)
    } else {
      Log.e(tag, message, throwable)
      emitRemote("ERROR", tag, "$message ${throwable.localizedMessage.orEmpty()}")
    }
  }

  internal fun redactForUpload(message: String): String {
    var redacted = message
    redacted =
        Regex(
                "(?i)((?:authorization|cookie|set-cookie|x-api-key|x-auth-token)\\s*[:=]\\s*(?:bearer\\s+)?)[^,\\s;]+")
            .replace(redacted, "$1[REDACTED]")
    redacted =
        Regex(
                "(?i)([\\\"']?\\b(?:sessionToken|session_token|authKey|auth_key|password|clientSecret|client_secret|accessToken|access_token|oauthSecret|oauth_secret|apiKey|api_key|auth-key|pairingCode|pairing_code|oneTimeCode|one_time_code|cookie|pin|token|code|secret)\\b[\\\"']?\\s*[:=]\\s*)([\\\"'][^\\\"']*[\\\"']|[^,\\s}&]+)")
            .replace(redacted, "$1[REDACTED]")
    return if (redacted.length > 2048) redacted.take(2048) + "…" else redacted
  }

  private fun emitRemote(level: String, tag: String?, message: String) {
    val component = tag.orEmpty().take(128)
    val safeMessage = redactForUpload(message)
    libtailscaleWrapper.sendLog(component, safeMessage)
    pinNodeLogSink?.invoke(
        PinNodeLogEntry(
            timestamp = Instant.now().toString(),
            level = level,
            component = component,
            message = safeMessage,
        ))
  }

  private fun isUnstableRelease(): Boolean {
    val versionName =
        appContext.packageManager.getPackageInfo(appContext.packageName, 0).versionName

    // Extract the middle number and check if it's odd
    val middleNumber = versionName?.split(".")?.getOrNull(1)?.toIntOrNull()
    return middleNumber?.let { it % 2 == 1 } ?: false
  }

  class LibtailscaleWrapper {
    public fun sendLog(tag: String?, message: String) {
      val logTag = tag ?: ""
      Libtailscale.sendLog((logTag + ": " + message).toByteArray(Charsets.UTF_8))
    }
  }
}
