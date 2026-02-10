#include <Arduino.h>
#include <NimBLEDevice.h>
#include <NimBLEAdvertisedDevice.h>
#include "NimBLEEddystoneTLM.h"
#include "NimBLEBeacon.h"

#define ENDIAN_CHANGE_U16(x) ((((x) & 0xFF00) >> 8) + (((x) & 0xFF) << 8))

// BLE scanning
#define BLE_SCAN_DURATION 2      // seconds per scan
#define BLE_SCAN_INTERVAL 3000   // ms between scans

static NimBLEScan* fyBLEScan = NULL;
static unsigned long fyLastBleScan = 0;


static std::string getUUID(const NimBLEAdvertisedDevice* device) {
    if (!device || !device->haveServiceUUID()) return "";
    int count = device->getServiceUUIDCount();
    if (count == 0) return "";
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

class FYBLECallbacks : public NimBLEScanCallbacks {
    void onResult(const NimBLEAdvertisedDevice* dev) override {
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
                "\"serviceUUID\":\"%s\",}\n",
                addrStr.c_str(), rssi, mfrCode, name.c_str(), serviceUuid.c_str());

    }
} scanCallbacks;

void setup() {
    Serial.begin(115200);
    delay(500);
    Serial.println("Scanning...");

    NimBLEDevice::init("");
    fyBLEScan = NimBLEDevice::getScan();
    // fyBLEScan->setAdvertisedDeviceCallbacks(new FYBLECallbacks());
    fyBLEScan->setScanCallbacks(new FYBLECallbacks());
    fyBLEScan->setActiveScan(true);
    fyBLEScan->setInterval(100);
    fyBLEScan->setWindow(99);

    // Kick off the first scan right away
    fyBLEScan->start(BLE_SCAN_DURATION, false);
    fyLastBleScan = millis();
    printf("[FLOCK-YOU] BLE scanning ACTIVE\n");
}

void loop() {
    // BLE scanning cycle
    if (millis() - fyLastBleScan >= BLE_SCAN_INTERVAL && !fyBLEScan->isScanning()) {
        fyBLEScan->start(BLE_SCAN_DURATION, false);
        fyLastBleScan = millis();
    }

    if (!fyBLEScan->isScanning() && millis() - fyLastBleScan > BLE_SCAN_DURATION * 1000) {
        fyBLEScan->clearResults();
    }

    delay(100);
}
