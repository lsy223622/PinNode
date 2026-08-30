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
  fun logRedactionCoversAdditionalCredentialAndPinForms() {
    val redacted =
        TSLog.redactForUpload(
            "accessToken=access-secret oauthSecret=oauth-secret apiKey=api-secret " +
                "pairingCode=123456 cookie=session-cookie")

    for (secret in
        listOf("access-secret", "oauth-secret", "api-secret", "123456", "session-cookie")) {
      assertFalse(redacted.contains(secret))
    }
  }

  @Test
  fun logBufferDropsOldEntriesAndAcknowledgesUploadedBatch() {
    val buffer = ClientLogBuffer(maxEntries = 2, maxCharacters = 1024)
    val first = buffer.append("session-a", ClientLogEntry("t1", "INFO", "test", "first"))
    val second = buffer.append("session-a", ClientLogEntry("t2", "INFO", "test", "second"))
    val third = buffer.append("session-a", ClientLogEntry("t3", "INFO", "test", "third"))

    assertTrue(first > 0)
    assertTrue(second > first)
    assertTrue(third > second)
    assertEquals(
        listOf("second", "third"), buffer.snapshot("session-a", 8).map { it.entry.message })
    assertEquals(
        listOf("session-a", "session-a"), buffer.snapshot("session-a", 8).map { it.sessionId })

    buffer.acknowledge(listOf(second))
    assertEquals(listOf("third"), buffer.snapshot("session-a", 8).map { it.entry.message })
    assertEquals(1, buffer.size())
  }

  @Test
  fun logBufferKeepsSessionAttributionAndCanDiscardFinishedSession() {
    val buffer = ClientLogBuffer(maxEntries = 8, maxCharacters = 1024)
    buffer.append("session-a", ClientLogEntry("t1", "INFO", "test", "old"))
    buffer.append("session-b", ClientLogEntry("t2", "INFO", "test", "new"))

    assertEquals(listOf("old"), buffer.snapshot("session-a", 8).map { it.entry.message })
    assertEquals(listOf("new"), buffer.snapshot("session-b", 8).map { it.entry.message })

    buffer.discardSession("session-a")
    assertEquals(emptyList<String>(), buffer.snapshot("session-a", 8).map { it.entry.message })
    assertEquals(listOf("new"), buffer.snapshot("session-b", 8).map { it.entry.message })
  }
}
