// Copyright (c) PinNode contributors
// SPDX-License-Identifier: BSD-3-Clause
package com.tailscale.ipn

import com.tailscale.ipn.util.TSLog
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class PinNodeTelemetryTest {

  @Test
  fun logRedactionRemovesCredentialsBeforeUpload() {
    val redacted =
        TSLog.redactForUpload(
            "authorization: Bearer bearer-secret sessionToken=\"session-secret\" password=pass-secret")

    assertFalse(redacted.contains("bearer-secret"))
    assertFalse(redacted.contains("session-secret"))
    assertFalse(redacted.contains("pass-secret"))
    assertEquals(3, redacted.split("[REDACTED]").size - 1)
  }

  @Test
  fun logBufferDropsOldEntriesAndAcknowledgesUploadedBatch() {
    val buffer = ClientLogBuffer(maxEntries = 2, maxCharacters = 1024)
    val first = buffer.append(ClientLogEntry("t1", "INFO", "test", "first"))
    val second = buffer.append(ClientLogEntry("t2", "INFO", "test", "second"))
    val third = buffer.append(ClientLogEntry("t3", "INFO", "test", "third"))

    assertTrue(first > 0)
    assertTrue(second > first)
    assertTrue(third > second)
    assertEquals(listOf("second", "third"), buffer.snapshot(8).map { it.entry.message })

    buffer.acknowledge(listOf(second))
    assertEquals(listOf("third"), buffer.snapshot(8).map { it.entry.message })
    assertEquals(1, buffer.size())
  }
}
