// Copyright (c) PinNode contributors
// SPDX-License-Identifier: BSD-3-Clause
package com.tailscale.ipn.ui.viewModel

import android.content.Intent
import androidx.lifecycle.ViewModel
import androidx.lifecycle.viewModelScope
import com.tailscale.ipn.App
import com.tailscale.ipn.RescueSessionManager.RescueConfigurationException
import com.tailscale.ipn.RescueSessionManager.RescueInvalidCodeException
import com.tailscale.ipn.RescueSessionManager.RescueServerHttpException
import com.tailscale.ipn.RescueTailscalePath
import com.tailscale.ipn.RescueSessionManager.RescueVpnPermissionException
import com.tailscale.ipn.R
import com.tailscale.ipn.ui.notifier.Notifier
import java.net.ConnectException
import java.net.NoRouteToHostException
import java.net.SocketException
import java.net.SocketTimeoutException
import java.net.UnknownHostException
import java.util.concurrent.TimeoutException
import javax.net.ssl.SSLException
import kotlinx.coroutines.TimeoutCancellationException
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

  private val _connecting = MutableStateFlow(false)
  val connecting: StateFlow<Boolean> = _connecting

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
    clearMessage()
  }

  fun clearMessage() {
    _message.value = null
  }

  fun onVpnPermissionResult(granted: Boolean) {
    sessionManager.onVpnPermissionResult(granted)
  }

  fun start(code: String) {
    if (_busy.value) return
    _busy.value = true
    _connecting.value = true
    _message.value = null
    sessionManager.setServerUrl(_serverUrl.value)
    viewModelScope.launch {
      try {
        val result = sessionManager.start(code)
        result.onFailure { error -> _message.value = userMessage(error, Operation.START) }
      } finally {
        _connecting.value = false
        _busy.value = false
      }
    }
  }

  fun stop() {
    if (_busy.value) return
    _busy.value = true
    _connecting.value = false
    _message.value = null
    viewModelScope.launch {
      try {
        val result = sessionManager.stop()
        result.onFailure { error -> _message.value = userMessage(error, Operation.STOP) }
      } finally {
        _busy.value = false
      }
    }
  }

  private enum class Operation {
    START,
    STOP,
  }

  private fun userMessage(error: Throwable, operation: Operation): String {
    val context = App.get()
    return when {
      error is RescueInvalidCodeException -> context.getString(R.string.rescue_error_invalid_code)
      error is RescueConfigurationException ->
          error.message ?: context.getString(R.string.rescue_error_configuration)
      error is RescueServerHttpException -> serverMessage(context, error.status)
      error is RescueVpnPermissionException ->
          context.getString(R.string.rescue_error_vpn_permission)
      error.hasCause<SSLException>() -> context.getString(R.string.rescue_error_tls)
      error.hasCause<TimeoutCancellationException>() || error.hasCause<TimeoutException>() ->
          context.getString(R.string.rescue_error_timeout)
      error.hasCause<UnknownHostException>() ||
          error.hasCause<ConnectException>() ||
          error.hasCause<NoRouteToHostException>() ||
          error.hasCause<SocketTimeoutException>() ||
          error.hasCause<SocketException>() -> networkMessage(context)
      error is IllegalArgumentException -> context.getString(R.string.rescue_error_configuration)
      operation == Operation.STOP -> context.getString(R.string.rescue_error_stop)
      else -> context.getString(R.string.rescue_error_start)
    }
  }

  private fun serverMessage(context: android.content.Context, status: Int): String =
      when (status) {
        400 -> context.getString(R.string.rescue_error_http_400)
        401 -> context.getString(R.string.rescue_error_http_401)
        403 -> context.getString(R.string.rescue_error_http_403)
        404 -> context.getString(R.string.rescue_error_http_404)
        409 -> context.getString(R.string.rescue_error_http_409)
        410 -> context.getString(R.string.rescue_error_http_410)
        429 -> context.getString(R.string.rescue_error_http_429)
        in 500..599 -> context.getString(R.string.rescue_error_http_5xx)
        else -> context.getString(R.string.rescue_error_http_other, status)
      }

  private fun networkMessage(context: android.content.Context): String =
      if (networkState.value.tailscalePath == RescueTailscalePath.WAITING_FOR_CELLULAR) {
        context.getString(R.string.rescue_error_cellular_network)
      } else {
        context.getString(R.string.rescue_error_network)
      }

  private inline fun <reified T : Throwable> Throwable.hasCause(): Boolean {
    val seen = mutableSetOf<Throwable>()
    var current: Throwable? = this
    while (current != null && seen.add(current)) {
      if (current is T) return true
      current = current.cause
    }
    return false
  }
}
