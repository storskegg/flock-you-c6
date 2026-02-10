#include <Arduino.h>
#include <NimBLEDevice.h>
#include <NimBLEAdvertisedDevice.h>
#include "NimBLEEddystoneTLM.h"
// #include "NimBLEBeacon.h"

// #define ENDIAN_CHANGE_U16(x) ((((x) & 0xFF00) >> 8) + (((x) & 0xFF) << 8))

// BLE scanning
#define BLE_SCAN_DURATION 5      // seconds per scan
#define BLE_SCAN_INTERVAL 6000   // ms between scans

// #define ANTENNA_USE_INTERNAL 1
#define ANTENNA_USE_EXTERNAL 1

#if !defined(ANTENNA_USE_INTERNAL) && !defined(ANTENNA_USE_EXTERNAL)
    #define ANTENNA_USE_INTERNAL 1
#endif

#if defined(ANTENNA_USE_INTERNAL) && defined(ANTENNA_USE_EXTERNAL)
    #error You must select INTERNAL or EXTERNAL antenna, not both
#endif

static NimBLEScan* fyBLEScan = NULL;
static unsigned long fyLastBleScan = 0;

void cfg_antenna(void) {
#ifdef ANTENNA_USE_INTERNAL
    pinMode(WIFI_ENABLE, OUTPUT);
    digitalWrite(WIFI_ENABLE, LOW);
    pinMode(WIFI_ANT_CONFIG, OUTPUT);
    digitalWrite(WIFI_ANT_CONFIG, LOW);
#elifdef ANTENNA_USE_EXTERNAL
    pinMode(WIFI_ENABLE, OUTPUT);
    digitalWrite(WIFI_ENABLE, LOW);
    pinMode(WIFI_ANT_CONFIG, OUTPUT);
    digitalWrite(WIFI_ANT_CONFIG, HIGH);
#endif
}

static std::string getUUID(const NimBLEAdvertisedDevice* device) {
    if (!device || !device->haveServiceUUID()) return "[]";
    int count = device->getServiceUUIDCount();
    if (count == 0) return "[]";
    int lastIdx = count-1;
    std::string uids = "[";
    for (int i = 0; i < count; i++) {
        NimBLEUUID svc = device->getServiceUUID(i);
        uids += "\"" + svc.toString() + "\"";
        if (i != lastIdx) uids += ",";
    }
    uids += "]";
    return uids;
}

void reportDevice(const NimBLEAdvertisedDevice* dev) {
    // Device MAC Address
    NimBLEAddress addr = dev->getAddress();
    std::string addrStr = addr.toString();

    // Device RSSI
    int rssi = dev->getRSSI();

    // Device Name
    std::string name = dev->haveName() ? dev->getName() : "";

    // Device Service UUID
    std::string serviceUuid = getUUID(dev);

    uint16_t mfrCode = 0;
    for (int i = 0; i < (int)dev->getManufacturerDataCount(); i++) {
        std::string data = dev->getManufacturerData(i);
        if (data.size() >= 2) {
            mfrCode = ((uint16_t)(uint8_t)data[1] << 8) |
                                (uint16_t)(uint8_t)data[0];
        }
    }

    printf("{\"protocol\":\"bluetooth_le\",\"mac_address\":\"%s\",\"rssi\":%d,\"mfrCode\":\"0x%04x\","
            "\"device_name\":\"%s\","
            "\"serviceUUID\":%s}\n",
            addrStr.c_str(), rssi, mfrCode, name.c_str(), serviceUuid.c_str());
}

class FYBLECallbacks : public NimBLEScanCallbacks {
    /** Initial discovery, advertisement data only. */
    void onDiscovered(const NimBLEAdvertisedDevice* dev) override {
        reportDevice(dev);
    }

    /**
     *  If active scanning the result here will have the scan response data.
     *  If not active scanning then this will be the same as onDiscovered.
     */
    // void onResult(const NimBLEAdvertisedDevice* dev) override {
    //     reportDevice(dev);
    // }

    void onScanEnd(const NimBLEScanResults& results, int reason) override {
        printf("{\"notification\": \"Scan ended reason = %d; restarting scan\"}\n", reason);
        NimBLEDevice::getScan()->start(BLE_SCAN_DURATION * 1000, false, true);
    }
} scanCallbacks;

void setup() {
    pinMode(LED_BUILTIN, OUTPUT);
    cfg_antenna();

    Serial.begin(115200);
    delay(500);
    Serial.println("Scanning...");

    NimBLEDevice::init("");
    NimBLEDevice::setScanDuplicateCacheSize(50);
    fyBLEScan = NimBLEDevice::getScan();
    fyBLEScan->setMaxResults(0);
    // fyBLEScan->setAdvertisedDeviceCallbacks(new FYBLECallbacks());
    fyBLEScan->setScanCallbacks(new FYBLECallbacks(), true);
    fyBLEScan->setActiveScan(true);
    fyBLEScan->setInterval(100);
    fyBLEScan->setWindow(99);

    // Kick off the first scan right away
    fyBLEScan->start(BLE_SCAN_DURATION, false, true);
    fyLastBleScan = millis();
    printf("[FLOCK-YOU] BLE scanning ACTIVE\n");
}

void loop() {
    // BLE scanning cycle
    if (fyBLEScan->isScanning()) {
        digitalWrite(LED_BUILTIN, HIGH);
    } else {
        digitalWrite(LED_BUILTIN, LOW);
    }

    // if (millis() - fyLastBleScan >= BLE_SCAN_INTERVAL && !fyBLEScan->isScanning()) {
    //     fyBLEScan->start(BLE_SCAN_DURATION, false);
    //     fyLastBleScan = millis();
    // }
    //
    // if (!fyBLEScan->isScanning() && millis() - fyLastBleScan > BLE_SCAN_DURATION * 1000) {
    //     fyBLEScan->clearResults();
    // }

    delay(100);
}
