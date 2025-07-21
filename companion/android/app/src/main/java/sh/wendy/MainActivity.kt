package sh.wendy

import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.navigation.NavType
import androidx.navigation.compose.NavHost
import androidx.navigation.compose.composable
import androidx.navigation.compose.rememberNavController
import androidx.navigation.navArgument
import sh.wendy.model.Device
import sh.wendy.ui.screens.DeviceDetailsScreen
import sh.wendy.ui.screens.DevicesListScreen
import sh.wendy.ui.theme.CompanionTheme

class MainActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        setContent {
            CompanionTheme {
                Surface(
                    modifier = Modifier.fillMaxSize(),
                    color = MaterialTheme.colorScheme.background
                ) {
                    CompanionApp()
                }
            }
        }
    }
}

@Composable
fun CompanionApp() {
    val navController = rememberNavController()
    
    NavHost(
        navController = navController,
        startDestination = "devices_list"
    ) {
        composable("devices_list") {
            DevicesListScreen(
                onDeviceClick = { device ->
                    navController.navigate("device_details/${device.id}/${device.name}")
                }
            )
        }
        
        composable(
            route = "device_details/{deviceId}/{deviceName}",
            arguments = listOf(
                navArgument("deviceId") { type = NavType.StringType },
                navArgument("deviceName") { type = NavType.StringType }
            )
        ) { backStackEntry ->
            val deviceId = backStackEntry.arguments?.getString("deviceId") ?: ""
            val deviceName = backStackEntry.arguments?.getString("deviceName") ?: ""
            
            DeviceDetailsScreen(
                device = Device(id = deviceId, name = deviceName),
                onNavigateBack = {
                    navController.popBackStack()
                }
            )
        }
    }
}