// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

package com.tailscale.ipn

import androidx.test.ext.junit.rules.activityScenarioRule
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import androidx.test.uiautomator.By
import androidx.test.uiautomator.UiDevice
import androidx.test.uiautomator.Until
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith

@RunWith(AndroidJUnit4::class)
class MainActivityTest {

  @get:Rule val activityRule = activityScenarioRule<MainActivity>()

  @Test
  fun showsPinNodeRescueScreen() {
    val device = UiDevice.getInstance(InstrumentationRegistry.getInstrumentation())

    assertTextVisible(device, "PinNode")
    assertAnyTextVisible(device, "Config API URL", "Configuration server")
    assertTextVisible(device, "Six-digit authorization code")
    assertTextVisible(device, "Confirm and start temporary node")
  }
}

private fun assertTextVisible(device: UiDevice, text: String) {
  assertTrue(
      "Expected to find '$text' on the PinNode rescue screen",
      device.wait(Until.hasObject(By.text(text)), 10_000))
}

private fun assertAnyTextVisible(device: UiDevice, vararg texts: String) {
  val found = texts.any { device.wait(Until.hasObject(By.text(it)), 10_000) }
  assertTrue(
      "Expected to find one of ${texts.joinToString()} on the PinNode rescue screen",
      found,
  )
}
