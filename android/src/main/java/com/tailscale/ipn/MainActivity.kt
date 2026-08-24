// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

package com.tailscale.ipn

import android.content.Context
import android.content.Intent
import android.content.RestrictionsManager
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Bundle
import android.os.Process
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.result.ActivityResultLauncher
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.ui.Modifier
import androidx.core.splashscreen.SplashScreen.Companion.installSplashScreen
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.lifecycleScope
import androidx.lifecycle.repeatOnLifecycle
import com.tailscale.ipn.mdm.MDMSettings
import com.tailscale.ipn.ui.theme.AppTheme
import com.tailscale.ipn.ui.util.universalFit
import com.tailscale.ipn.ui.view.RescueView
import com.tailscale.ipn.ui.viewModel.AppViewModel
import com.tailscale.ipn.util.ShareFileHelper
import com.tailscale.ipn.util.TSLog
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch

class MainActivity : ComponentActivity() {
  private lateinit var appViewModel: AppViewModel
  private lateinit var directoryPickerLauncher: ActivityResultLauncher<Uri?>

  override fun onCreate(savedInstanceState: Bundle?) {
    installSplashScreen()
    super.onCreate(savedInstanceState)

    App.get()
    appViewModel = App.get().getAppScopedViewModel()
    directoryPickerLauncher =
        registerForActivityResult(ActivityResultContracts.OpenDocumentTree()) { uri ->
          if (uri == null) return@registerForActivityResult
          try {
            contentResolver.takePersistableUriPermission(
                uri,
                Intent.FLAG_GRANT_READ_URI_PERMISSION or Intent.FLAG_GRANT_WRITE_URI_PERMISSION,
            )
          } catch (e: SecurityException) {
            TSLog.e("MainActivity", "Failed to persist Taildrop directory permission: $e")
          }
          val writePermission =
              checkUriPermission(
                  uri, Process.myPid(), Process.myUid(), Intent.FLAG_GRANT_WRITE_URI_PERMISSION)
          if (writePermission != PackageManager.PERMISSION_GRANTED) {
            TSLog.w("MainActivity", "Taildrop directory is not writable: $uri")
            return@registerForActivityResult
          }
          lifecycleScope.launch(Dispatchers.IO) {
            try {
              TaildropDirectoryStore.saveFileDirectory(uri)
              ShareFileHelper.notifyDirectoryReady()
              ShareFileHelper.setUri(uri.toString())
            } catch (e: Exception) {
              TSLog.e("MainActivity", "Failed to save Taildrop directory: $e")
            }
          }
        }
    appViewModel.directoryPickerLauncher = directoryPickerLauncher
    lifecycleScope.launch {
      repeatOnLifecycle(Lifecycle.State.STARTED) {
        appViewModel.triggerDirectoryPicker.collect { directoryPickerLauncher.launch(null) }
      }
    }
    setContent {
      AppTheme {
        Surface(color = MaterialTheme.colorScheme.inverseSurface) {
          Surface(modifier = Modifier.universalFit()) { RescueView() }
        }
      }
    }
  }

  private fun updateMdmSettings() {
    val restrictionsManager = getSystemService(Context.RESTRICTIONS_SERVICE) as RestrictionsManager
    lifecycleScope.launch(Dispatchers.IO) { MDMSettings.update(App.get(), restrictionsManager) }
  }

  override fun onResume() {
    super.onResume()
    updateMdmSettings()
  }

  override fun onStop() {
    super.onStop()
    updateMdmSettings()
  }

  override fun onDestroy() {
    if (isFinishing) {
      App.get().getRescueSessionManager().exitForAppCloseBlocking()
    }
    super.onDestroy()
  }
}
