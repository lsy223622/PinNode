// Copyright (c) Tailscale Inc & AUTHORS
// SPDX-License-Identifier: BSD-3-Clause

package com.tailscale.ipn.ui.view

import androidx.compose.foundation.Canvas
import androidx.compose.material3.MaterialTheme
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.drawscope.Stroke
import com.tailscale.ipn.ui.theme.onBackgroundLogoDotDisabled
import com.tailscale.ipn.ui.theme.onBackgroundLogoDotEnabled
import com.tailscale.ipn.ui.theme.standaloneLogoDotDisabled
import com.tailscale.ipn.ui.theme.standaloneLogoDotEnabled
import com.tailscale.ipn.ui.util.set
import kotlin.concurrent.timer
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow

// DotsMatrix represents the state of the progress indicator.
typealias DotsMatrix = List<List<Boolean>>

val logoDotsMatrix: DotsMatrix =
    listOf(
        listOf(true, true, false),
        listOf(true, true, false),
        listOf(true, false, false),
    )

@Composable
fun TailscaleLogoView(
    animated: Boolean = false,
    usesOnBackgroundColors: Boolean = false,
    modifier: Modifier
) {

  val primaryColor: Color =
      if (usesOnBackgroundColors) {
        MaterialTheme.colorScheme.onBackgroundLogoDotEnabled
      } else {
        MaterialTheme.colorScheme.standaloneLogoDotEnabled
      }
  val secondaryColor: Color =
      if (usesOnBackgroundColors) {
        MaterialTheme.colorScheme.onBackgroundLogoDotDisabled
      } else {
        MaterialTheme.colorScheme.standaloneLogoDotDisabled
      }

  val currentDotsMatrix: StateFlow<DotsMatrix> = MutableStateFlow(logoDotsMatrix)
  var currentDotsMatrixIndex = 0
  fun advanceToNextMatrix() {
    currentDotsMatrixIndex = (currentDotsMatrixIndex + 1) % gameOfLife.size
    val newMatrix =
        if (animated) {
          gameOfLife[currentDotsMatrixIndex]
        } else {
          logoDotsMatrix
        }
    currentDotsMatrix.set(newMatrix)
  }

  if (animated) {
    timer(period = 300L) { advanceToNextMatrix() }
  }

  val currentMatrix = currentDotsMatrix.collectAsState().value
  Canvas(modifier = modifier) {
    val side = size.minDimension
    val left = (size.width - side) / 2
    val top = (size.height - side) / 2
    val xPositions = floatArrayOf(0.296875f, 0.50006944f, 0.703125f)
    val yPositions = floatArrayOf(0.29680556f, 0.5f, 0.70305556f)
    for (y in 0..2) {
      for (x in 0..2) {
        drawCircle(
            color = if (currentMatrix[y][x]) primaryColor else secondaryColor,
            radius = side * 0.05370833f,
            center = Offset(left + side * xPositions[x], top + side * yPositions[y]),
            style = Stroke(width = side * 0.028f),
        )
      }
    }
  }
}

val gameOfLife: List<DotsMatrix> =
    listOf(
        listOf(
            listOf(false, true, true),
            listOf(true, false, true),
            listOf(false, false, true),
        ),
        listOf(
            listOf(false, true, true),
            listOf(false, false, true),
            listOf(false, true, false),
        ),
        listOf(
            listOf(false, true, true),
            listOf(false, false, false),
            listOf(false, false, true),
        ),
        listOf(
            listOf(false, false, true),
            listOf(false, true, false),
            listOf(false, false, false),
        ),
        listOf(
            listOf(false, true, false),
            listOf(false, false, false),
            listOf(false, false, false),
        ),
        listOf(
            listOf(false, false, false),
            listOf(false, false, true),
            listOf(false, false, false),
        ),
        listOf(
            listOf(false, false, false),
            listOf(false, false, false),
            listOf(false, false, false),
        ),
        listOf(
            listOf(false, false, true),
            listOf(false, false, false),
            listOf(false, false, false),
        ),
        listOf(
            listOf(false, false, false),
            listOf(false, false, false),
            listOf(true, false, false),
        ),
        listOf(listOf(false, false, false), listOf(false, false, false), listOf(true, true, false)),
        listOf(listOf(false, false, false), listOf(true, false, false), listOf(true, true, false)),
        listOf(listOf(false, false, false), listOf(true, true, false), listOf(false, true, false)),
        listOf(listOf(false, false, false), listOf(true, true, false), listOf(false, true, true)),
        listOf(listOf(false, false, false), listOf(true, true, true), listOf(false, false, true)),
        listOf(listOf(false, true, false), listOf(true, true, true), listOf(true, false, true)))
