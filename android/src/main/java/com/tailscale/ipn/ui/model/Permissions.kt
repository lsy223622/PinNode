// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

package com.tailscale.ipn.ui.model

import android.Manifest
import androidx.compose.runtime.Composable
import com.google.accompanist.permissions.ExperimentalPermissionsApi
import com.google.accompanist.permissions.PermissionState
import com.google.accompanist.permissions.isGranted
import com.google.accompanist.permissions.rememberMultiplePermissionsState
import com.google.accompanist.permissions.shouldShowRationale
import com.tailscale.ipn.R

object Permissions {
  /** Permissions to prompt for on MainView. */
  @OptIn(ExperimentalPermissionsApi::class)
  val prompt: List<Pair<Permission, PermissionState>>
    @Composable
    get() {
      val permissionStates = rememberMultiplePermissionsState(permissions = all.map { it.name })
      return all.zip(permissionStates.permissions).filter { (_, state) ->
        !state.status.isGranted && !state.status.shouldShowRationale
      }
    }

  /** All permissions with granted status. */
  @OptIn(ExperimentalPermissionsApi::class)
  val withGrantedStatus: List<Pair<Permission, Boolean>>
    @Composable
    get() {
      val permissionStates = rememberMultiplePermissionsState(permissions = all.map { it.name })
      val result = mutableListOf<Pair<Permission, Boolean>>()
      result.addAll(
          all.zip(permissionStates.permissions).map { (permission, state) ->
            Pair(permission, state.status.isGranted)
          })
      return result
    }

  /**
   * All permissions that Tailscale requires. MainView takes care of prompting for permissions, and
   * PermissionsView provides a list of permissions with corresponding statuses and a link to the
   * application settings.
   *
   * When new permissions are needed, just add them to this list and the necessary strings to
   * strings.xml and the rest should take care of itself.
   */
  private val all: List<Permission> by lazy {
    listOf(
        Permission(
            Manifest.permission.POST_NOTIFICATIONS,
            R.string.permission_post_notifications,
            R.string.permission_post_notifications_needed))
  }
}

data class Permission(
    val name: String,
    val title: Int,
    val description: Int,
)
