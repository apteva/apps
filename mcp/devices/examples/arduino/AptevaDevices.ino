// ESP32/ESP8266 example for the Apteva Devices app.
// Libraries: PubSubClient and ArduinoJson 6.x.

#if defined(ESP8266)
#include <ESP8266WiFi.h>
#else
#include <WiFi.h>
#endif
#include <PubSubClient.h>
#include <ArduinoJson.h>
#include <LittleFS.h>
#include <time.h>

const char* WIFI_SSID = "replace-me";
const char* WIFI_PASSWORD = "replace-me";
const char* MQTT_HOST = "your-apteva-host";
const uint16_t MQTT_PORT = 1883;
const char* DEVICE_ID = "esp32-1";
const char* MQTT_USERNAME = "scope-qualified-username-from-enrollment";
const char* TOPIC_BASE = "scopes/scope-token/devices/esp32-1";
const char* MQTT_CLIENT_ID = "unique-client-id-for-this-credential";
const char* DEVICE_PASSWORD = "one-time-password-from-provisioning";

constexpr int LED_PIN = 2;
#if defined(ESP8266)
constexpr int SENSOR_PIN = A0;
#else
constexpr int SENSOR_PIN = 34; // Change for your ESP32 board when needed.
#endif

WiFiClient network;
PubSubClient mqtt(network);
unsigned long lastTelemetry = 0;

String topic(const char* suffix) { return String(TOPIC_BASE) + "/" + suffix; }
int currentJournalSlot = -1;
String currentRequest;
uint64_t currentExpiry = 0;

bool saveJournal(int slot, const String& commandId, const String& state, const String& response) {
  StaticJsonDocument<2048> record;
  record["command_id"] = commandId; record["request"] = currentRequest;
  record["state"] = state; record["response"] = response; record["expires_ms"] = currentExpiry;
  String path = String("/cmd") + slot + ".json";
  File file = LittleFS.open(path, "w");
  if (!file) return false;
  size_t written = serializeJson(record, file); file.flush(); file.close();
  return written > 0 && !record.overflowed();
}


bool allowedPin(int pin, bool write) {
  if (pin == LED_PIN) return true;
  if (pin == SENSOR_PIN) return !write;
  return false;
}

void publishResponse(const char* commandId, bool success, JsonVariantConst result, const char* error = "") {
  StaticJsonDocument<512> response;
  response["command_id"] = commandId;
  response["device_id"] = DEVICE_ID;
  response["success"] = success;
  if (success) response["result"].set(result);
  if (!success) response["error"] = error;
  response["timestamp"] = millis();
  char payload[512];
  serializeJson(response, payload, sizeof(payload));
  if (currentJournalSlot >= 0 && !saveJournal(currentJournalSlot, commandId, "completed", String(payload))) return;
  mqtt.publish(topic("response").c_str(), payload, false);
}

void failCommand(const char* commandId, const String& message) {
  StaticJsonDocument<64> empty;
  publishResponse(commandId, false, empty.as<JsonVariantConst>(), message.c_str());
}

void handleCommand(char*, byte* bytes, unsigned int length) {
  if (length >= 1024) return;
  StaticJsonDocument<1024> command;
  if (deserializeJson(command, bytes, length)) return;
  const char* commandId = command["command_id"] | "";
  if (!commandId[0]) commandId = command["id"] | "";
  currentJournalSlot = -1;
  if (strncmp(commandId, "cmd_", 4) || strlen(commandId) > 124) return;
  for (const char* ch = commandId; *ch; ++ch) if (!isalnum(*ch) && *ch != '_' && *ch != '-') return;
  uint64_t nowMs = uint64_t(time(nullptr)) * 1000;
  currentExpiry = command["expires_unix_ms"] | uint64_t(0);
  // A synchronized wall clock is required before physical actions.
  if (nowMs < 1700000000000ULL) { failCommand(commandId, "clock not synchronized; not executed"); return; }
  if (!currentExpiry || currentExpiry <= nowMs) { failCommand(commandId, "command expired; not executed"); return; }
  currentRequest = ""; serializeJson(command, currentRequest);
  int freeSlot = -1;
  for (int slot = 0; slot < 32; ++slot) {
    String path = String("/cmd") + slot + ".json";
    if (!LittleFS.exists(path)) { if (freeSlot < 0) freeSlot = slot; continue; }
    File file = LittleFS.open(path, "r"); StaticJsonDocument<2048> record;
    DeserializationError error = deserializeJson(record, file); file.close();
    // Corrupt records fail closed; never interpret corruption as an unused slot.
    if (error) { failCommand(commandId, "command journal needs repair; not executed"); return; }
    if (String(record["command_id"] | "") == commandId) {
      if (String(record["request"] | "") != currentRequest) { failCommand(commandId, "command_id conflict; not executed"); return; }
      if (String(record["state"] | "") == "completed") mqtt.publish(topic("response").c_str(), record["response"] | "", false);
      else failCommand(commandId, "execution unconfirmed after interruption; inspect hardware");
      return;
    }
    uint64_t expiry = record["expires_ms"] | uint64_t(0);
    if (expiry && expiry + 86400000ULL < nowMs && freeSlot < 0) freeSlot = slot;
  }
  if (freeSlot < 0) { failCommand(commandId, "command journal full; not executed"); return; }
  currentJournalSlot = freeSlot;
  if (!saveJournal(freeSlot, commandId, "executing", "")) { currentJournalSlot = -1; failCommand(commandId, "cannot persist claim; not executed"); return; }
  const char* operation = command["operation"] | command["type"] | "";
  JsonObject args = command["arguments"].is<JsonObject>() ? command["arguments"].as<JsonObject>() : command["params"].as<JsonObject>();
  StaticJsonDocument<512> result;

  if (!strcmp(operation, "device.info") || !strcmp(operation, "get_info")) {
    result["id"] = DEVICE_ID; result["name"] = "Workshop controller"; result["hardware"] = "ESP32"; result["connected"] = true;
    result["variables"]["sensor"] = analogRead(SENSOR_PIN);
    result["functions"].add("set_led");
  } else if (!strcmp(operation, "variable.get") || !strcmp(operation, "get_variable")) {
    const char* name = command["target"] | args["name"] | args["variable"] | "";
    if (strcmp(name, "sensor")) { failCommand(commandId, "unknown variable"); return; }
    result["variable"] = name; result["value"] = analogRead(SENSOR_PIN);
  } else if (!strcmp(operation, "function.call") || !strcmp(operation, "function")) {
    const char* name = command["target"] | args["name"] | args["function"] | "";
    if (strcmp(name, "set_led")) { failCommand(commandId, "unknown function"); return; }
    int value = args["value"] | 1;
    digitalWrite(LED_PIN, value ? HIGH : LOW);
    result["executed"] = name; result["return_value"] = value ? 1 : 0;
  } else if (!strcmp(operation, "pin.read") || strstr(operation, "_read")) {
    int pin = args["pin"] | command["pin"] | -1;
    if (!allowedPin(pin, false)) { failCommand(commandId, "pin is not allowlisted"); return; }
    result["pin"] = pin; result["return_value"] = pin == SENSOR_PIN ? analogRead(pin) : digitalRead(pin);
  } else if (!strcmp(operation, "pin.write") || strstr(operation, "_write")) {
    int pin = args["pin"] | command["pin"] | -1;
    int value = args["value"] | command["value"] | 0;
    if (!allowedPin(pin, true)) { failCommand(commandId, "pin is not writable"); return; }
    digitalWrite(pin, value ? HIGH : LOW); result["pin"] = pin; result["return_value"] = value ? 1 : 0;
  } else if (!strcmp(operation, "pin.mode") || !strcmp(operation, "pin_mode")) {
    int pin = args["pin"] | command["pin"] | -1;
    const char* mode = args["mode"] | command["mode"] | "";
    if (!allowedPin(pin, false)) { failCommand(commandId, "pin is not allowlisted"); return; }
    if (!strcmp(mode, "output")) pinMode(pin, OUTPUT);
    else if (!strcmp(mode, "input")) pinMode(pin, INPUT);
    else if (!strcmp(mode, "input_pullup")) pinMode(pin, INPUT_PULLUP);
    else { failCommand(commandId, "invalid pin mode"); return; }
    result["pin"] = pin; result["mode"] = mode;
  } else { failCommand(commandId, "unsupported operation"); return; }
  publishResponse(commandId, true, result.as<JsonVariantConst>());
}

void publishManifest() {
  StaticJsonDocument<768> doc;
  doc["protocol"] = "apteva.devices/v1"; doc["command_contract"] = "apteva.commands/v1"; doc["name"] = "Workshop controller"; doc["model"] = "ESP32";
  JsonObject variable = doc["variables"].createNestedObject(); variable["name"] = "sensor"; variable["type"] = "number"; variable["readable"] = true;
  JsonObject function = doc["functions"].createNestedObject(); function["name"] = "set_led";
  JsonObject led = doc["pins"].createNestedObject(); led["name"] = "status_led"; led["number"] = LED_PIN; led["type"] = "digital"; led["writable"] = true; led["modes"].add("input"); led["modes"].add("output");
  JsonObject sensor = doc["pins"].createNestedObject(); sensor["name"] = "sensor"; sensor["number"] = SENSOR_PIN; sensor["type"] = "analog"; sensor["writable"] = false; sensor["modes"].add("input");
  char payload[768]; serializeJson(doc, payload, sizeof(payload)); mqtt.publish(topic("manifest").c_str(), payload, true);
}

void connectMQTT() {
  while (!mqtt.connected()) {
    String clientId = MQTT_CLIENT_ID;
    if (mqtt.connect(clientId.c_str(), MQTT_USERNAME, DEVICE_PASSWORD, topic("availability").c_str(), 1, true, "offline")) {
      mqtt.subscribe(topic("commands").c_str(), 1); mqtt.publish(topic("availability").c_str(), "online", true); publishManifest();
    } else delay(2000);
  }
}

void setup() {
  if (!LittleFS.begin()) { while (true) delay(1000); } // Never auto-format a command journal.
  pinMode(LED_PIN, OUTPUT); pinMode(SENSOR_PIN, INPUT);
  WiFi.begin(WIFI_SSID, WIFI_PASSWORD); while (WiFi.status() != WL_CONNECTED) delay(250);
  configTime(0, 0, "pool.ntp.org", "time.nist.gov");
  mqtt.setServer(MQTT_HOST, MQTT_PORT); mqtt.setCallback(handleCommand); mqtt.setBufferSize(1536); connectMQTT();
}

void loop() {
  if (!mqtt.connected()) connectMQTT(); mqtt.loop();
  if (millis() - lastTelemetry >= 10000) {
    lastTelemetry = millis(); StaticJsonDocument<128> data; data["sensor"] = analogRead(SENSOR_PIN); data["uptime_ms"] = millis();
    char payload[128]; serializeJson(data, payload, sizeof(payload)); mqtt.publish(topic("telemetry").c_str(), payload, false);
  }
}
