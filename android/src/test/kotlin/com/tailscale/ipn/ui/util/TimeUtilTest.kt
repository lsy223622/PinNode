// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

package com.tailscale.ipn.ui.util

import com.tailscale.ipn.util.TSLog
import com.tailscale.ipn.util.TSLog.LibtailscaleWrapper
import java.time.Duration
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Before
import org.junit.Test
import org.mockito.ArgumentMatchers.anyString
import org.mockito.Mockito.doNothing
import org.mockito.Mockito.mock

class TimeUtilTest {

  private lateinit var libtailscaleWrapperMock: LibtailscaleWrapper
  private lateinit var originalWrapper: LibtailscaleWrapper

  @Before
  fun setUp() {
    libtailscaleWrapperMock = mock(LibtailscaleWrapper::class.java)
    doNothing().`when`(libtailscaleWrapperMock).sendLog(anyString(), anyString())

    originalWrapper = TSLog.libtailscaleWrapper
    TSLog.libtailscaleWrapper = libtailscaleWrapperMock
  }

  @After
  fun tearDown() {
    TSLog.libtailscaleWrapper = originalWrapper
  }

  @Test
  fun durationInvalidMsUnits() {
    val actual = TimeUtil.duration("5s10ms")
    assertNull("Should return null", actual)
  }

  @Test
  fun durationInvalidUsUnits() {
    val actual = TimeUtil.duration("5s10us")
    assertNull("Should return null", actual)
  }

  @Test
  fun durationTestHappyPath() {
    val input = arrayOf("1.0y1.0w1.0d1.0h1.0m1.0s", "1s", "1m", "1h", "1d", "1w", "1y")
    val expectedSeconds =
        arrayOf((31536000 + 604800 + 86400 + 3600 + 60 + 1), 1, 60, 3600, 86400, 604800, 31536000)
    val expected = expectedSeconds.map { Duration.ofSeconds(it.toLong()) }
    val actual = input.map { TimeUtil.duration(it) }
    assertEquals("Incorrect conversion", expected, actual)
  }

  @Test
  fun testBadDurationString() {
    val actual = TimeUtil.duration("1..0y1.0w1.0d1.0h1.0m1.0s")
    assertNull("Should return null", actual)
  }

  @Test
  fun testBadDInputString() {
    val actual = TimeUtil.duration("1.0yy1.0w1.0d1.0h1.0m1.0s")
    assertNull("Should return null", actual)
  }

  @Test
  fun testIgnoreFractionalSeconds() {
    val actual = TimeUtil.duration("10.9s")
    assertEquals("Should return 10 seconds", Duration.ofSeconds(10), actual)
  }
}
