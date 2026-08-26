// Copyright (c) PinNode contributors
// SPDX-License-Identifier: BSD-3-Clause
package com.tailscale.ipn.ui.view

import android.app.Activity
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material3.Button
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.CenterAlignedTopAppBar
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TopAppBarDefaults
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.unit.dp
import androidx.lifecycle.viewmodel.compose.viewModel
import com.tailscale.ipn.R
import com.tailscale.ipn.RescueLinkState
import com.tailscale.ipn.RescueNetworkState
import com.tailscale.ipn.RescueSessionState
import com.tailscale.ipn.RescueTailscalePath
import com.tailscale.ipn.ui.theme.errorButton
import com.tailscale.ipn.ui.viewModel.RescueViewModel

@Composable
fun RescueView(onNavigateBack: (() -> Unit)? = null, viewModel: RescueViewModel = viewModel()) {
  var code by remember { mutableStateOf("") }
  val vpnPermissionLauncher =
      rememberLauncherForActivityResult(ActivityResultContracts.StartActivityForResult()) { result
        ->
        viewModel.onVpnPermissionResult(result.resultCode == Activity.RESULT_OK)
      }
  val busy by viewModel.busy.collectAsState()
  val session by viewModel.sessionState.collectAsState()
  val network by viewModel.networkState.collectAsState()
  val netmap by viewModel.netmap.collectAsState()
  val message by viewModel.message.collectAsState()
  val serverUrl by viewModel.serverUrl.collectAsState()

  LaunchedEffect(Unit) {
    viewModel.vpnPermissionRequests.collect { intent -> vpnPermissionLauncher.launch(intent) }
  }
  LaunchedEffect(session.active) { if (session.active) code = "" }

  Scaffold(topBar = { PinNodeHeader(onNavigateBack) }) { innerPadding ->
    Column(
        modifier =
            Modifier.padding(innerPadding)
                .fillMaxSize()
                .background(MaterialTheme.colorScheme.surface)
                .verticalScroll(rememberScrollState())
                .padding(horizontal = 16.dp, vertical = 12.dp),
        verticalArrangement = Arrangement.spacedBy(12.dp),
    ) {
      SessionStatusCard(session = session, network = network)
      NetworkStatusCard(
          session = session,
          network = network,
          tailscaleIp =
              netmap?.SelfNode?.primaryIPv4Address ?: netmap?.SelfNode?.primaryIPv6Address,
      )

      if (!session.active) {
        StartSessionCard(
            serverUrl = serverUrl,
            serverLocked = viewModel.serverLocked,
            code = code,
            busy = busy,
            onServerUrlChange = viewModel::setServerUrl,
            onCodeChange = { value ->
              code = value.filter(Char::isDigit).take(6)
              viewModel.clearMessage()
            },
            onStart = {
              val submittedCode = code
              code = ""
              viewModel.start(submittedCode)
            },
        )
      } else {
        RoutingStatusCard(session = session)
        Button(
            onClick = viewModel::stop,
            enabled = !busy,
            colors = MaterialTheme.colorScheme.errorButton,
            modifier = Modifier.fillMaxWidth(),
        ) {
          Text(stringResource(R.string.rescue_node_stop))
        }
      }

      message?.let { text ->
        Surface(
            color = MaterialTheme.colorScheme.errorContainer,
            contentColor = MaterialTheme.colorScheme.onErrorContainer,
            shape = MaterialTheme.shapes.medium,
            modifier = Modifier.fillMaxWidth(),
        ) {
          Text(
              text = text,
              style = MaterialTheme.typography.bodyMedium,
              modifier = Modifier.padding(12.dp),
          )
        }
      }
    }
  }
}

@OptIn(ExperimentalMaterial3Api::class)
@Composable
private fun PinNodeHeader(onNavigateBack: (() -> Unit)?) {
  CenterAlignedTopAppBar(
      title = {
        Text(
            text = stringResource(R.string.rescue_node_title),
            style = MaterialTheme.typography.titleLarge,
        )
      },
      navigationIcon = {
        onNavigateBack?.let { navigateBack ->
          IconButton(onClick = navigateBack) {
            Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = null)
          }
        }
      },
      colors =
          TopAppBarDefaults.centerAlignedTopAppBarColors(
              containerColor = MaterialTheme.colorScheme.surface,
              scrolledContainerColor = MaterialTheme.colorScheme.surface,
          ),
  )
}

@Composable
private fun SessionStatusCard(session: RescueSessionState, network: RescueNetworkState) {
  val active = session.active
  val waitingForWifi =
      active &&
          session.subnetRouterEnabled &&
          session.subnetRoutes.isNotEmpty() &&
          !network.wifi.connected
  val waitingForCellular =
      active &&
          session.networkMode == "cellular" &&
          network.tailscalePath == RescueTailscalePath.WAITING_FOR_CELLULAR
  val title =
      when {
        waitingForCellular -> stringResource(R.string.rescue_status_waiting_cellular)
        waitingForWifi -> stringResource(R.string.rescue_status_waiting_wifi)
        active -> stringResource(R.string.rescue_status_running)
        else -> stringResource(R.string.rescue_status_idle)
      }
  val detail =
      when {
        waitingForCellular -> stringResource(R.string.rescue_status_waiting_cellular_detail)
        waitingForWifi -> stringResource(R.string.rescue_status_waiting_wifi_detail)
        active -> stringResource(R.string.rescue_node_running)
        else -> stringResource(R.string.rescue_node_explanation)
      }
  val statusColor =
      when {
        waitingForWifi -> MaterialTheme.colorScheme.tertiary
        active -> MaterialTheme.colorScheme.primary
        else -> MaterialTheme.colorScheme.outline
      }

  Card(
      colors =
          CardDefaults.cardColors(
              containerColor =
                  if (active) MaterialTheme.colorScheme.primaryContainer
                  else MaterialTheme.colorScheme.surfaceVariant),
      modifier = Modifier.fillMaxWidth(),
  ) {
    Row(
        modifier = Modifier.fillMaxWidth().padding(16.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
      StatusDot(color = statusColor)
      Spacer(Modifier.width(12.dp))
      Column(verticalArrangement = Arrangement.spacedBy(3.dp)) {
        Text(
            text = title,
            style = MaterialTheme.typography.titleMedium,
            fontWeight = FontWeight.SemiBold,
        )
        Text(
            text = detail,
            style = MaterialTheme.typography.bodySmall,
            color = MaterialTheme.colorScheme.onSurfaceVariant,
        )
      }
    }
  }
}

@Composable
private fun NetworkStatusCard(
    session: RescueSessionState,
    network: RescueNetworkState,
    tailscaleIp: String?,
) {
  Card(modifier = Modifier.fillMaxWidth()) {
    Column(modifier = Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
      SectionTitle(stringResource(R.string.rescue_network_title))
      Row(modifier = Modifier.fillMaxWidth()) {
        NetworkStatus(
            title = stringResource(R.string.rescue_wifi),
            state = network.wifi,
            modifier = Modifier.weight(1f),
        )
        Box(
            modifier =
                Modifier.padding(horizontal = 12.dp)
                    .width(1.dp)
                    .height(72.dp)
                    .background(MaterialTheme.colorScheme.outlineVariant))
        NetworkStatus(
            title = stringResource(R.string.rescue_mobile_data),
            state = network.cellular,
            modifier = Modifier.weight(1f),
        )
      }
      if (session.active) {
        HorizontalDivider()
        SettingRow(
            label = stringResource(R.string.rescue_tailscale_path),
            value = tailscalePathText(session, network),
        )
        SettingRow(
            label = stringResource(R.string.rescue_tailscale_ip),
            value = tailscaleIp ?: stringResource(R.string.rescue_none),
        )
      }
    }
  }
}

@Composable
private fun NetworkStatus(title: String, state: RescueLinkState, modifier: Modifier = Modifier) {
  Column(modifier = modifier, verticalArrangement = Arrangement.spacedBy(3.dp)) {
    Row(verticalAlignment = Alignment.CenterVertically) {
      StatusDot(
          color =
              if (state.connected) MaterialTheme.colorScheme.primary
              else MaterialTheme.colorScheme.outline)
      Spacer(Modifier.width(7.dp))
      Text(title, style = MaterialTheme.typography.titleSmall, fontWeight = FontWeight.SemiBold)
    }
    Text(
        text =
            stringResource(
                if (state.connected) R.string.rescue_connected else R.string.rescue_disconnected),
        style = MaterialTheme.typography.bodySmall,
    )
    Text(
        text =
            stringResource(
                if (state.internetAvailable) R.string.rescue_internet_available
                else R.string.rescue_internet_unavailable),
        style = MaterialTheme.typography.bodySmall,
        color = MaterialTheme.colorScheme.onSurfaceVariant,
    )
    state.interfaceName?.let { interfaceName ->
      Text(
          text = interfaceName,
          style = MaterialTheme.typography.labelSmall,
          color = MaterialTheme.colorScheme.outline,
      )
    }
  }
}

@Composable
private fun tailscalePathText(session: RescueSessionState, network: RescueNetworkState): String {
  if (!session.vpnEnabled) return stringResource(R.string.rescue_disabled)
  return when (network.tailscalePath) {
    RescueTailscalePath.CELLULAR -> stringResource(R.string.rescue_path_cellular)
    RescueTailscalePath.WAITING_FOR_CELLULAR ->
        stringResource(R.string.rescue_path_waiting_cellular)
    RescueTailscalePath.DEFAULT -> stringResource(R.string.rescue_path_default)
  }
}

@Composable
private fun RoutingStatusCard(session: RescueSessionState) {
  Card(modifier = Modifier.fillMaxWidth()) {
    Column(modifier = Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
      SectionTitle(stringResource(R.string.rescue_routes_title))
      SettingRow(
          label = stringResource(R.string.rescue_subnet_router),
          value =
              stringResource(
                  if (session.subnetRouterEnabled) R.string.rescue_enabled
                  else R.string.rescue_disabled),
      )
      if (session.subnetRouterEnabled) {
        SettingRow(
            label = stringResource(R.string.rescue_subnet_routes),
            value =
                session.subnetRoutes.takeIf(List<String>::isNotEmpty)?.joinToString("\n")
                    ?: stringResource(R.string.rescue_none),
        )
      }
      SettingRow(
          label = stringResource(R.string.rescue_use_exit_node),
          value =
              when {
                !session.exitNodeEnabled -> stringResource(R.string.rescue_disabled)
                else -> session.exitNodeSelector ?: stringResource(R.string.rescue_enabled)
              },
      )
      SettingRow(
          label = stringResource(R.string.rescue_advertise_exit_node),
          value =
              stringResource(
                  if (session.advertiseExitNode) R.string.rescue_enabled
                  else R.string.rescue_disabled),
      )
    }
  }
}

@Composable
private fun StartSessionCard(
    serverUrl: String,
    serverLocked: Boolean,
    code: String,
    busy: Boolean,
    onServerUrlChange: (String) -> Unit,
    onCodeChange: (String) -> Unit,
    onStart: () -> Unit,
) {
  Card(modifier = Modifier.fillMaxWidth()) {
    Column(modifier = Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
      SectionTitle(stringResource(R.string.rescue_node_start))
      OutlinedTextField(
          value = serverUrl,
          onValueChange = onServerUrlChange,
          label = {
            Text(
                stringResource(
                    if (serverLocked) R.string.rescue_node_server
                    else R.string.rescue_node_server_url))
          },
          placeholder = {
            Text(
                stringResource(R.string.rescue_node_server_url_hint),
                style = MaterialTheme.typography.bodySmall,
            )
          },
          keyboardOptions =
              KeyboardOptions(
                  keyboardType = if (serverLocked) KeyboardType.Text else KeyboardType.Uri),
          singleLine = true,
          enabled = !busy && !serverLocked,
          modifier = Modifier.fillMaxWidth(),
      )
      OutlinedTextField(
          value = code,
          onValueChange = onCodeChange,
          label = { Text(stringResource(R.string.rescue_node_pin)) },
          keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Number),
          singleLine = true,
          enabled = !busy,
          modifier = Modifier.fillMaxWidth(),
      )
      Button(
          onClick = onStart,
          enabled = !busy && code.length == 6,
          modifier = Modifier.fillMaxWidth(),
      ) {
        Text(
            stringResource(
                if (busy) R.string.rescue_connecting else R.string.rescue_node_confirm),
        )
      }
    }
  }
}

@Composable
private fun SettingRow(label: String, value: String) {
  Row(
      modifier = Modifier.fillMaxWidth(),
      horizontalArrangement = Arrangement.spacedBy(16.dp),
      verticalAlignment = Alignment.Top,
  ) {
    Text(
        text = label,
        style = MaterialTheme.typography.bodyMedium,
        color = MaterialTheme.colorScheme.onSurfaceVariant,
        modifier = Modifier.weight(1f),
    )
    Text(
        text = value,
        style = MaterialTheme.typography.bodyMedium,
        fontWeight = FontWeight.Medium,
        modifier = Modifier.weight(1f),
    )
  }
}

@Composable
private fun SectionTitle(text: String) {
  Text(text = text, style = MaterialTheme.typography.titleSmall, fontWeight = FontWeight.SemiBold)
}

@Composable
private fun StatusDot(color: Color) {
  Box(modifier = Modifier.size(10.dp).clip(CircleShape).background(color))
}
