#include <Arduino.h>
#include <base64.h>
#include <NimBLEDevice.h>
#include <NimBLEAdvertisedDevice.h>
#include <SparkFun_u-blox_GNSS_Arduino_Library.h>
#include <FastLED.h>
#include <sys/time.h>
#include <atomic>
#include <esp_task_wdt.h>

// BLE scanning
#define BLE_SCAN_DURATION 5      // seconds per scan

// #define ANTENNA_USE_INTERNAL 1
#define ANTENNA_USE_EXTERNAL 1

#if !defined(ANTENNA_USE_INTERNAL) && !defined(ANTENNA_USE_EXTERNAL)
    #define ANTENNA_USE_INTERNAL 1
#endif

#if defined(ANTENNA_USE_INTERNAL) && defined(ANTENNA_USE_EXTERNAL)
    #error You must select INTERNAL or EXTERNAL antenna, not both
#endif

// GNSS
#define GNSS_RX_PIN       15
#define GNSS_TX_PIN       16
#define GNSS_BAUD         9600
#define GNSS_POLL_MS      1000     // matches SAM-M8Q 1Hz nav rate
#define RTC_SYNC_INTERVAL_MS (10UL * 60UL * 1000UL)  // 10 minutes

// LED (WS2812 on GPIO48)
#define LED_PIN           48       // raw GPIO, NOT LED_BUILTIN (which is virtual)
#define NUM_LEDS          1
#define LED_BRIGHTNESS    32
#define BLINK_INTERVAL_MS 500      // "medium" blink speed

//////////////////////////////////////
// PerfectHashSet

namespace lookup {

    class PerfectHashSet {
    private:
        static constexpr const char* entries_[] = {
            "0xfe07",   // Sonos
            "0xfe0f",   // Phillips Lighting
            "0xfeb9"
        };

        static constexpr uint8_t size_ = std::size(entries_);

    public:
        static bool contains(const char* str) {
            for (const auto entry : entries_) {
                if (strcmp(entry, str) == 0) {
                    return true;
                }
            }
            return false;
        }

        static int8_t index_of(const char* str) {
            for (int8_t i = 0; i < size_; i++) {
                if (strcmp(entries_[i], str) == 0) {
                    return i;
                }
            }
            return -1;
        }

        static const char* at(const uint8_t idx) {
            return idx < size_ ? entries_[idx] : nullptr;
        }

        static constexpr uint8_t size() { return size_; }
    };

    constexpr const char* PerfectHashSet::entries_[];

} // namespace lookup

//////////////////////////////////////
// GNSS shared state

struct GNSSState {
    int32_t  latitude;      // degrees * 1e-7
    int32_t  longitude;     // degrees * 1e-7
    uint32_t hAccuracy;     // millimeters
    uint8_t  fixType;       // 0=none, 2=2D, 3=3D, 5=time-only
    uint8_t  siv;           // satellites in view (summed across constellations)
    uint16_t year;
    uint8_t  month, day, hour, minute, second;
};

static SemaphoreHandle_t gnssMutex = nullptr;
static GNSSState gnssShared = {};
static std::atomic<uint8_t> gnssFixType{0};

// GNSS hardware
static SFE_UBLOX_GNSS gnss;
static HardwareSerial gnssSerial(1);  // UART1

// LED
static CRGB leds[NUM_LEDS];

// RTC sync tracking
static unsigned long lastRtcSyncMs = 0;

// BLE
static NimBLEScan* fyBLEScan = nullptr;

//////////////////////////////////////
// Antenna configuration

void cfg_antenna() {
#if defined(WIFI_ENABLE) && defined(WIFI_ANT_CONFIG)
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
#endif
}

//////////////////////////////////////
// LED state machine

static CRGB fixTypeColor(uint8_t ft) {
    switch (ft) {
        case 3:  return CRGB::Green;
        case 2:  return CRGB::Yellow;
        case 5:  return CRGB(255, 165, 0);  // orange
        default: return CRGB::Red;
    }
}

static bool fixTypeBlinks(uint8_t ft) {
    return ft != 3;  // only 3D fix is solid
}

static void updateLED() {
    uint8_t ft = gnssFixType.load(std::memory_order_acquire);
    CRGB color = fixTypeColor(ft);

    if (fixTypeBlinks(ft)) {
        bool on = (millis() / BLINK_INTERVAL_MS) % 2 == 0;
        leds[0] = on ? color : CRGB::Black;
    } else {
        leds[0] = color;
    }
    FastLED.show();
}

//////////////////////////////////////
// RTC sync

static void syncRTC(const GNSSState& state) {
    struct tm tm = {};
    tm.tm_year = state.year - 1900;
    tm.tm_mon  = state.month - 1;
    tm.tm_mday = state.day;
    tm.tm_hour = state.hour;
    tm.tm_min  = state.minute;
    tm.tm_sec  = state.second;

    time_t epoch = mktime(&tm);
    struct timeval tv = { .tv_sec = epoch, .tv_usec = 0 };
    settimeofday(&tv, nullptr);
}

//////////////////////////////////////
// ISO 8601 timestamp helper

static void formatISO8601(char* buf, size_t bufLen, const GNSSState& s) {
    snprintf(buf, bufLen, "%04u-%02u-%02uT%02u:%02u:%02uZ",
             s.year, s.month, s.day, s.hour, s.minute, s.second);
}

//////////////////////////////////////
// GNSS state reader (populates a GNSSState from the library)

static GNSSState readGNSSFromModule() {
    GNSSState local = {};
    local.fixType   = gnss.getFixType();
    local.latitude  = gnss.getLatitude();
    local.longitude = gnss.getLongitude();
    local.hAccuracy = gnss.getHorizontalAccEst();
    local.siv       = gnss.getSIV();
    local.year      = gnss.getYear();
    local.month     = gnss.getMonth();
    local.day       = gnss.getDay();
    local.hour      = gnss.getHour();
    local.minute    = gnss.getMinute();
    local.second    = gnss.getSecond();
    return local;
}

//////////////////////////////////////
// GNSS FreeRTOS task (core 1)

static void gnssTask(void* param) {
    (void)param;

    for (;;) {
        gnss.checkUblox();

        if (gnss.getPVT()) {
            GNSSState local = readGNSSFromModule();

            // Update shared state under mutex
            if (xSemaphoreTake(gnssMutex, pdMS_TO_TICKS(50)) == pdTRUE) {
                gnssShared = local;
                xSemaphoreGive(gnssMutex);
            }

            // Lock-free update for LED
            gnssFixType.store(local.fixType, std::memory_order_release);

            // Periodic RTC sync
            unsigned long now = millis();
            if (local.fixType >= 2 && (now - lastRtcSyncMs >= RTC_SYNC_INTERVAL_MS)) {
                syncRTC(local);
                lastRtcSyncMs = now;
            }
        }

        vTaskDelay(pdMS_TO_TICKS(GNSS_POLL_MS));
    }
}

//////////////////////////////////////
// BLE helpers

static std::tuple<bool, std::string> getUUIDs(const NimBLEAdvertisedDevice* device) {
    if (!device || !device->haveServiceUUID()) return {false, "[]"};
    const int count = device->getServiceUUIDCount();
    if (count == 0) return {false, "[]"};
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
    // Gate on GNSS fix: read shared state under mutex
    GNSSState gps;
    if (xSemaphoreTake(gnssMutex, pdMS_TO_TICKS(10)) == pdTRUE) {
        gps = gnssShared;
        xSemaphoreGive(gnssMutex);
    } else {
        return;  // mutex timeout — skip this frame
    }

    if (gps.fixType != 2 && gps.fixType != 3) {
        return;  // no spatial fix — no output
    }

    // Existing filtering logic
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
    if (omit) return;

    const std::string mfrDataB64 = !mfrDataRaw.empty() ? Base64::encode(mfrDataRaw) : "";
    std::string addrStr = dev->getAddress().toString();
    const int8_t rssi = dev->getRSSI();
    const std::string name = dev->haveName() ? dev->getName() : "";

    // Format GNSS fields
    char timestamp[21];
    formatISO8601(timestamp, sizeof(timestamp), gps);

    double lat = gps.latitude / 1e7;
    double lon = gps.longitude / 1e7;

    // Emit JSON with GNSS fields
    printf("{\"mac_address\":\"%s\",\"rssi\":%d,\"mfr_code\":%u,"
            "\"device_name\":\"%s\","
            "\"service_uuids\":%s,\"mfr_data\":\"%s\","
            "\"lat\":%.7f,\"lon\":%.7f,\"fix\":%u,"
            "\"h_acc\":%u,\"siv\":%u,\"timestamp\":\"%s\"}\n",
            addrStr.c_str(), rssi, mfrCode, name.c_str(),
            serviceUuids.c_str(), mfrDataB64.c_str(),
            lat, lon, gps.fixType,
            gps.hAccuracy, gps.siv, timestamp);
}

//////////////////////////////////////
// BLE callbacks

class FYBLECallbacks : public NimBLEScanCallbacks {
    void onDiscovered(const NimBLEAdvertisedDevice* dev) override {
        reportDevice(dev);
    }

    void onScanEnd(const NimBLEScanResults& results, const int reason) override {
        printf("{\"notification\": \"Scan ended reason = %d; restarting scan\"}\n", reason);
        NimBLEDevice::getScan()->start(BLE_SCAN_DURATION * 1000, false, true);
    }
} scanCallbacks;

//////////////////////////////////////
// setup

void setup() {
    // Set timezone for RTC (must be before any time functions)
    setenv("TZ", "UTC0", 1);
    tzset();

    // Initialize LED
    FastLED.addLeds<WS2812, LED_PIN, GRB>(leds, NUM_LEDS);
    FastLED.setBrightness(LED_BRIGHTNESS);
    leds[0] = CRGB::Black;
    FastLED.show();

    cfg_antenna();

    Serial.begin(115200);
    delay(1000);
    Serial.println(R"({"notification":"Initializing..."})");

    // Create GNSS mutex
    gnssMutex = xSemaphoreCreateMutex();

    // Initialize GNSS UART and library
    gnssSerial.begin(GNSS_BAUD, SERIAL_8N1, GNSS_RX_PIN, GNSS_TX_PIN);

    while (!gnss.begin(gnssSerial)) {
        Serial.println(R"({"notification":"GNSS module not found, retrying..."})");
        updateLED();
        delay(1000);
    }

    gnss.setUART1Output(COM_TYPE_UBX);
    gnss.setNavigationFrequency(1);
    gnss.saveConfigSelective(VAL_CFG_SUBSEC_IOPORT);

    Serial.println(R"({"notification":"GNSS initialized, waiting for fix..."})");

    // Block until we acquire a spatial fix (2D or 3D).
    // Use polling mode here — getPVT() sends an explicit poll request and
    // waits for the response.  Auto PVT is enabled later for the background task.
    // A poll timeout means the UART link has degraded (not just "no fix yet").
    uint8_t pvtTimeouts = 0;
    while (true) {
        if (gnss.getPVT()) {
            pvtTimeouts = 0;  // link is healthy
            GNSSState local = readGNSSFromModule();
            gnssFixType.store(local.fixType, std::memory_order_release);

            Serial.printf("{\"notification\":\"GNSS fix PENDING, type=%u, siv=%u\"}\n",
                              local.fixType, local.siv);

            if (local.fixType >= 2 && local.fixType <= 3) {
                gnssShared = local;  // no mutex needed yet (task not started)

                syncRTC(local);
                lastRtcSyncMs = millis();

                Serial.printf("{\"notification\":\"GNSS fix ACQUIRED, type=%u, siv=%u\"}\n",
                              local.fixType, local.siv);

                break;
            }
        } else {
            pvtTimeouts++;
            Serial.printf("{\"notification\":\"GNSS poll TIMEOUT (%u consecutive)\"}\n",
                              pvtTimeouts);

            if (pvtTimeouts >= 10) {
                Serial.println(R"({"notification":"GNSS UART link FAILURE, re-initializing..."})");
                gnss.begin(gnssSerial);
                pvtTimeouts = 0;
            }
        }

        updateLED();
        delay(500);
    }

    updateLED();

    // Enable auto PVT for the background GNSS task (implicitUpdate=false
    // means the task must call checkUblox() before getPVT())
    gnss.setAutoPVT(true, false);

    // Launch GNSS FreeRTOS task on core 1
    xTaskCreatePinnedToCore(
        gnssTask,
        "gnss",
        4096,
        nullptr,
        1,
        nullptr,
        1
    );

    // Remove core 0's idle task from the task watchdog.
    // Core 0 is dedicated to NimBLE, which legitimately saturates the CPU
    // during active scanning — IDLE0 starvation is expected, not a fault.
    esp_task_wdt_delete(xTaskGetIdleTaskHandleForCPU(0));

    // Initialize BLE (NimBLE host runs on core 0)
    NimBLEDevice::init("");
    NimBLEDevice::setScanDuplicateCacheSize(50);
    fyBLEScan = NimBLEDevice::getScan();
    fyBLEScan->setMaxResults(0);
    fyBLEScan->setScanCallbacks(new FYBLECallbacks(), true);
    fyBLEScan->setActiveScan(true);
    fyBLEScan->setInterval(100);
    fyBLEScan->setWindow(99);

    fyBLEScan->start(BLE_SCAN_DURATION, false, true);
    printf("{\"notification\":\"BLE scanning ACTIVE\"}\n");
}

//////////////////////////////////////
// loop

void loop() {
    updateLED();
    delay(50);
}
