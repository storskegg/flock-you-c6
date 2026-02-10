#include <Arduino.h>
#include <NimBLEDevice.h>
#include <NimBLEAdvertisedDevice.h>
#include "NimBLEEddystoneTLM.h"
#include "NimBLEBeacon.h"

#define ENDIAN_CHANGE_U16(x) ((((x) & 0xFF00) >> 8) + (((x) & 0xFF) << 8))

// BLE scanning
#define BLE_SCAN_DURATION 2      // seconds per scan
#define BLE_SCAN_INTERVAL 3000   // ms between scans

int         scanTime = 5 * 1000; // In milliseconds
static NimBLEScan* fyBLEScan = NULL;
static unsigned long fyLastBleScan = 0;

class FYBLECallbacks : public NimBLEScanCallbacks {
    void onResult(const NimBLEAdvertisedDevice* dev) override {
        // Device MAC Address

        NimBLEAddress addr = dev->getAddress();
        std::string addrStr = addr.toString();

        // Safe MAC byte extraction
        // unsigned int m[6];
        // sscanf(addrStr.c_str(), "%02x:%02x:%02x:%02x:%02x:%02x",
        //        &m[0], &m[1], &m[2], &m[3], &m[4], &m[5]);
        // uint8_t mac[6] = {(uint8_t)m[0], (uint8_t)m[1], (uint8_t)m[2],
        //                   (uint8_t)m[3], (uint8_t)m[4], (uint8_t)m[5]};

        // Device RSSI
        int rssi = dev->getRSSI();

        // Device Name
        std::string name = dev->haveName() ? dev->getName() : "";

        //////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////////
        // Device Service UUID

        std::string serviceUuid = dev->haveServiceUUID() ? dev->getServiceUUID().toString() : "";
        
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
                addrStr.c_str(), rssi, mfrCode, name.c_str(), serviceUuid);

        // if (dev->haveServiceUUID()) {
        //     NimBLEUUID devUUID = dev->getServiceUUID();
        //     Serial.print("Found ServiceUUID: ");
        //     Serial.println(devUUID.toString().c_str());
        //     Serial.println("");
        // } else if (dev->haveManufacturerData() == true) {
        //     std::string strManufacturerData = dev->getManufacturerData();
        //     if (strManufacturerData.length() == 25 && strManufacturerData[0] == 0x4C && strManufacturerData[1] == 0x00) {
        //         Serial.println("Found an iBeacon!");
        //         NimBLEBeacon oBeacon = NimBLEBeacon();
        //         oBeacon.setData(reinterpret_cast<const uint8_t*>(strManufacturerData.data()), strManufacturerData.length());
        //         Serial.printf("iBeacon Frame\n");
        //         Serial.printf("ID: %04X Major: %d Minor: %d UUID: %s Power: %d\n",
        //                       oBeacon.getManufacturerId(),
        //                       ENDIAN_CHANGE_U16(oBeacon.getMajor()),
        //                       ENDIAN_CHANGE_U16(oBeacon.getMinor()),
        //                       oBeacon.getProximityUUID().toString().c_str(),
        //                       oBeacon.getSignalPower());
        //     } else {
        //         Serial.println("Found another manufacturers beacon!");
        //         Serial.printf("strManufacturerData: %d ", strManufacturerData.length());
        //         for (int i = 0; i < strManufacturerData.length(); i++) {
        //             Serial.printf("[%X]", strManufacturerData[i]);
        //         }
        //         Serial.printf("\n");
        //     }
        //     return;
        // }

        // NimBLEUUID eddyUUID = (uint16_t)0xfeaa;

        // if (advertisedDevice->getServiceUUID().equals(eddyUUID)) {
        //     std::string serviceData = advertisedDevice->getServiceData(eddyUUID);
        //     if (serviceData[0] == 0x20) {
        //         Serial.println("Found an EddystoneTLM beacon!");
        //         NimBLEEddystoneTLM foundEddyTLM = NimBLEEddystoneTLM();
        //         foundEddyTLM.setData(reinterpret_cast<const uint8_t*>(serviceData.data()), serviceData.length());

        //         Serial.printf("Reported battery voltage: %dmV\n", foundEddyTLM.getVolt());
        //         Serial.printf("Reported temperature from TLM class: %.2fC\n", (double)foundEddyTLM.getTemp());
        //         int   temp     = (int)serviceData[5] + (int)(serviceData[4] << 8);
        //         float calcTemp = temp / 256.0f;
        //         Serial.printf("Reported temperature from data: %.2fC\n", calcTemp);
        //         Serial.printf("Reported advertise count: %d\n", foundEddyTLM.getCount());
        //         Serial.printf("Reported time since last reboot: %ds\n", foundEddyTLM.getTime());
        //         Serial.println("\n");
        //         Serial.print(foundEddyTLM.toString().c_str());
        //         Serial.println("\n");
        //     }
        // }
    }
} scanCallbacks;


static bool checkUUID(NimBLEAdvertisedDevice* device, char* out_uuid = nullptr) {
    if (!device || !device->haveServiceUUID()) return false;
    int count = device->getServiceUUIDCount();
    if (count == 0) return false;
    for (int i = 0; i < count; i++) {
        NimBLEUUID svc = device->getServiceDataUUID(i);
        std::string str = svc.toString();
        for (size_t j = 0; j < sizeof())
    }
}

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


    // NimBLEScanResults foundDevices = fyBLEScan->getResults(scanTime, false);
    // Serial.print("Devices found: ");
    // Serial.println(foundDevices.getCount());
    // Serial.println("Scan done!");
    // pBLEScan->clearResults(); // delete results scan buffer to release memory
    delay(100);
}
