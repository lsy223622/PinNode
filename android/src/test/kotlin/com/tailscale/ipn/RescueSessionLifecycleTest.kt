// Copyright (c) PinNode contributors
// SPDX-License-Identifier: BSD-3-Clause

package com.tailscale.ipn

import org.junit.Assert.assertEquals
import org.junit.Test

class RescueSessionLifecycleTest {

  @Test
  fun repeatedLifecycleHandoffKeepsOnePendingSession() {
    val first = upsertPendingCleanupSessionIDs(emptyList(), "session-a")
    val second = upsertPendingCleanupSessionIDs(first, "session-a")
    val third = upsertPendingCleanupSessionIDs(listOf("session-a", "session-b"), "session-a")

    assertEquals(listOf("session-a"), second)
    assertEquals(listOf("session-b", "session-a"), third)
  }
}
