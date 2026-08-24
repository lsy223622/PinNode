// Copyright (c) PinNode contributors
// SPDX-License-Identifier: BSD-3-Clause
package com.tailscale.ipn.ui.viewModel

import android.content.Intent
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import com.tailscale.ipn.App
import com.tailscale.ipn.ui.notifier.Notifier
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.SharedFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.launch

class RescueViewModel : ViewModel() {
  private val sessionManager
    get() = App.get().getRescueSessionManager()

  val vpnPermissionRequests: SharedFlow<Intent>
    get() = sessionManager.vpnPermissionRequests

  private val _busy = MutableStateFlow(false)
  val busy: StateFlow<Boolean> = _busy

  val sessionState
    get() = sessionManager.sessionState

  val networkState
    get() = App.get().rescueNetworkState()

  val netmap = Notifier.netmap

  private val _message = MutableStateFlow<String?>(null)
  val message: StateFlow<String?> = _message

  val serverLocked: Boolean = sessionManager.isServerLocked()

  private val _serverUrl = MutableStateFlow(sessionManager.configuredServerDisplay())
  val serverUrl: StateFlow<String> = _serverUrl

  fun setServerUrl(value: String) {
    _serverUrl.value = value
    sessionManager.setServerUrl(value)
  }

  fun onVpnPermissionResult(granted: Boolean) {
    sessionManager.onVpnPermissionResult(granted)
  }

  fun start(code: String) {
    if (_busy.value) return
    _busy.value = true
    _message.value = null
    sessionManager.setServerUrl(_serverUrl.value)
    viewModelScope.launch {
      val result = sessionManager.start(code)
      result.onFailure { error -> _message.value = error.message ?: "启动临时会话失败" }
      _busy.value = false
    }
  }

  fun stop() {
    if (_busy.value) return
    _busy.value = true
    _message.value = null
    viewModelScope.launch {
      val result = sessionManager.stop()
      result.onFailure { error -> _message.value = error.message ?: "停止临时会话失败" }
      _busy.value = false
    }
  }
}
