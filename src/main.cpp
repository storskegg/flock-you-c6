#include <Arduino.h>
#include <NimBLEDevice.h>

// FreeRTOS task handles
TaskHandle_t bleScanTaskHandle = NULL;
TaskHandle_t displayTaskHandle = NULL;

// BLE scanner
NimBLEScan* pBLEScan;

// Shared data with mutex protection
SemaphoreHandle_t deviceCountMutex;
int deviceCount = 0;

// BLE Scan Task
void bleScanTask(void *parameter) {
    Serial.println("BLE Scan Task started");
    
    // Initialize BLE
    NimBLEDevice::init("");
    pBLEScan = NimBLEDevice::getScan();
    pBLEScan->setActiveScan(true);
    pBLEScan->setInterval(100);
    pBLEScan->setWindow(99);
    
    while (1) {
        Serial.println("Starting BLE scan...");
        NimBLEScanResults foundDevices = pBLEScan->start(5, false);
        
        // Update shared variable with mutex
        if (xSemaphoreTake(deviceCountMutex, portMAX_DELAY) == pdTRUE) {
            deviceCount = foundDevices.getCount();
            xSemaphoreGive(deviceCountMutex);
        }
        
        pBLEScan->clearResults();
        
        // Wait 10 seconds before next scan
        vTaskDelay(pdMS_TO_TICKS(10000));
    }
}

// Display Task
void displayTask(void *parameter) {
    Serial.println("Display Task started");
    
    while (1) {
        int count = 0;
        
        // Read shared variable with mutex
        if (xSemaphoreTake(deviceCountMutex, portMAX_DELAY) == pdTRUE) {
            count = deviceCount;
            xSemaphoreGive(deviceCountMutex);
        }
        
        Serial.print("Devices found: ");
        Serial.println(count);
        Serial.print("Free heap: ");
        Serial.println(ESP.getFreeHeap());
        
        // Update every 2 seconds
        vTaskDelay(pdMS_TO_TICKS(2000));
    }
}

void setup() {
    Serial.begin(115200);
    delay(1000);
    
    Serial.println("\n\nESP32-C6 BLE Scanner with FreeRTOS");
    Serial.print("CPU Frequency: ");
    Serial.print(getCpuFrequencyMhz());
    Serial.println(" MHz");
    
    // Create mutex for thread-safe data sharing
    deviceCountMutex = xSemaphoreCreateMutex();
    
    if (deviceCountMutex == NULL) {
        Serial.println("Failed to create mutex!");
        while (1);
    }
    
    // Create BLE scan task on Core 0
    xTaskCreatePinnedToCore(
        bleScanTask,           // Task function
        "BLE_Scan",            // Task name
        4096,                  // Stack size (bytes)
        NULL,                  // Parameter
        2,                     // Priority (0-24, higher = more priority)
        &bleScanTaskHandle,    // Task handle
        0                      // Core ID (0 or 1)
    );
    
    // Create display task on Core 1
    xTaskCreatePinnedToCore(
        displayTask,
        "Display",
        2048,
        NULL,
        1,
        &displayTaskHandle,
        1
    );
    
    Serial.println("Tasks created successfully");
}

void loop() {
    // Empty - all work is done in tasks
    // This loop still runs but we're not using it
    vTaskDelay(pdMS_TO_TICKS(1000));
}