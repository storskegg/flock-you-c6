#include <Arduino.h>
#include <base64.h>
#include <NimBLEDevice.h>
#include <NimBLEAdvertisedDevice.h>
// #include "NimBLEEddystoneTLM.h"
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

//////////////////////////////////////
// PerfectHashSet

namespace lookup {

    // Perfect Hash Set - O(1) lookup
    class PerfectHashSet {
    private:
        // Your strings - replace with actual values
        static constexpr const char* entries_[] = {
            "0xfe0f"    // Phillips Lighting
        };

        static constexpr uint8_t size_ = std::size(entries_);

    public:
        // Check if string exists - O(1)
        static bool contains(const char* str) {
            for (const auto entry : entries_) {
                if (strcmp(entry, str) == 0) {
                    return true;
                }
            }
            return false;
        }

        // Get index if exists - returns -1 if not found
        static int8_t index_of(const char* str) {
            for (int8_t i = 0; i < size_; i++) {
                if (strcmp(entries_[i], str) == 0) {
                    return i;
                }
            }
            return -1;
        }

        // Get string by index
        static const char* at(const uint8_t idx) {
            return idx < size_ ? entries_[idx] : nullptr;
        }

        static constexpr uint8_t size() { return size_; }
    };

    // Initialize the static member
    constexpr const char* PerfectHashSet::entries_[];

} // namespace lookup


//////////////////////////////////

static NimBLEScan* fyBLEScan = nullptr;
static unsigned long fyLastBleScan = 0;

void cfg_antenna() {
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

static std::tuple<bool, std::string> getUUIDs(const NimBLEAdvertisedDevice* device) {
    if (!device || !device->haveServiceUUID()) return {false, "[]"};
    const int count = device->getServiceUUIDCount();
    if (count == 0) return {false, "[]"};;
    const int lastIdx = count-1;
    std::string uids = "[";
    for (int i = 0; i < count; i++) {
        std::string svc = device->getServiceUUID(i).toString();
        if (lookup::PerfectHashSet::contains(svc.c_str())) {
            return {true, ""};
        }
        uids += "\"" + svc + "\"";
        if (i != lastIdx) uids += ",";
    }
    uids += "]";
    return {false, uids};
}

void reportDevice(const NimBLEAdvertisedDevice* dev) {
    const std::string mfrDataRaw = dev->getManufacturerData();

    // return quick if we're handling an iBeacon; they're just noise
    if (mfrDataRaw.length() == 25 && mfrDataRaw[0] == 0x4C && mfrDataRaw[1] == 0x00) {
        return;
    }

    // derive manufacturer code from mfr data, if present
    uint16_t mfrCode = 0;
    if (mfrDataRaw.length() > 1) {
        mfrCode = (static_cast<uint16_t>(static_cast<uint8_t>(mfrDataRaw[1])) << 8) |
                                static_cast<uint16_t>(static_cast<uint8_t>(mfrDataRaw[0]));
    }

    // return quick-ish if handling some unknown beacon
    if (mfrCode == 0x004C) {
        return;
    }

    auto [omit, serviceUuids] = getUUIDs(dev);

    const std::string mfrDataB64 = !mfrDataRaw.empty() ? Base64::encode(mfrDataRaw) : "";

    // Device MAC Address
    std::string addrStr = dev->getAddress().toString();

    // Device RSSI
    const int8_t rssi = dev->getRSSI();

    // Device Name
    const std::string name = dev->haveName() ? dev->getName() : "";

    printf("{\"mac_address\":\"%s\",\"rssi\":%d,\"mfr_code\":%u,"
            "\"device_name\":\"%s\","
            "\"service_uuids\":%s,\"mfr_data\":\"%s\"}\n",
            addrStr.c_str(), rssi, mfrCode, name.c_str(), serviceUuids.c_str(), mfrDataB64.c_str());
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

    void onScanEnd(const NimBLEScanResults& results, const int reason) override {
        printf("{\"notification\": \"Scan ended reason = %d; restarting scan\"}\n", reason);
        NimBLEDevice::getScan()->start(BLE_SCAN_DURATION * 1000, false, true);
    }
} scanCallbacks;

void setup() {
    pinMode(LED_BUILTIN, OUTPUT);
    cfg_antenna();

    Serial.begin(115200);
    delay(500);
    Serial.println(R"({"notification":"initializing ble scanner..."})");

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
    printf("{\"notification\":\"BLE scanning ACTIVE\"}\n");
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
