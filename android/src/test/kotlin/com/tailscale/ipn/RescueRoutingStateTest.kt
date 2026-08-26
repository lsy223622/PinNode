// Copyright (c) PinNode contributors
// SPDX-License-Identifier: BSD-3-Clause

package com.tailscale.ipn

import com.tailscale.ipn.ui.model.Ipn
import com.tailscale.ipn.ui.model.Netmap
import com.tailscale.ipn.ui.model.Tailcfg
import org.junit.Assert.assertEquals
import org.junit.Test

class RescueRoutingStateTest {

  @Test
  fun unapprovedExitNodeIsNotEffective() {
    val peer =
        Tailcfg.Node(
            StableID = "exit-node",
            Addresses = listOf("100.64.0.2"),
            AllowedIPs = listOf("100.64.0.2/32"),
            Online = true,
        )

    val observation =
        observeRescueExitNode(
            RescueExitNodeSelection(enabled = true, ip = "100.64.0.2"),
            Ipn.State.Running,
            Ipn.Prefs(ExitNodeID = "exit-node", ExitNodeIP = "100.64.0.2"),
            networkMap(peer),
        )

    assertEquals(RescueExitNodeStatus.UNAVAILABLE, observation.status)
  }

  @Test
  fun approvedOnlineExitNodeIsEffective() {
    val peer =
        Tailcfg.Node(
            StableID = "exit-node",
            Addresses = listOf("100.64.0.2"),
            AllowedIPs = listOf("100.64.0.2/32", "0.0.0.0/0", "::/0"),
            Online = true,
        )

    val observation =
        observeRescueExitNode(
            RescueExitNodeSelection(enabled = true, id = "exit-node"),
            Ipn.State.Running,
            Ipn.Prefs(ExitNodeID = "exit-node"),
            networkMap(peer),
        )

    assertEquals(RescueExitNodeStatus.ACTIVE, observation.status)
  }

  @Test
  fun advertisedExitNodeWithoutApprovalIsPending() {
    val observation =
        observeRescueAdvertiseExitNode(
            requested = true,
            backendState = Ipn.State.Running,
            prefs = Ipn.Prefs(AdvertiseRoutes = listOf("0.0.0.0/0", "::/0")),
            netmap = networkMap(Tailcfg.Node(StableID = "self")),
        )

    assertEquals(RescueAdvertiseExitNodeStatus.PENDING_APPROVAL, observation)
  }

  private fun networkMap(peer: Tailcfg.Node): Netmap.NetworkMap =
      Netmap.NetworkMap(
          SelfNode = Tailcfg.Node(StableID = "self"),
          Peers = listOf(peer),
          Domain = "",
          UserProfiles = emptyMap(),
          TKAEnabled = false,
      )
}
