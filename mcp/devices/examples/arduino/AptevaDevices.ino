// ESP32/ESP8266 example for the Apteva Devices app.
// Libraries: PubSubClient and ArduinoJson 6.x.

#if defined(ESP8266)
#include <ESP8266WiFi.h>
#else
#include <WiFi.h>
#endif
#include <PubSubClient.h>
#include <ArduinoJson.h>

const char* WIFI_SSID = "replace-me";
const char* WIFI_PASSWORD = "replace-me";
const char* MQTT_HOST = "your-apteva-host";
const uint16_t MQTT_PORT = 1883;
const char* DEVICE_ID = "esp32-1";
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

String topic(const char* suffix) { return String("devices/") + DEVICE_ID + "/" + suffix; }

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
  const char* commandId = command["command_id"] | command["id"] | "";
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
  doc["protocol"] = "apteva.devices/v1"; doc["name"] = "Workshop controller"; doc["model"] = "ESP32";
  JsonObject variable = doc["variables"].createNestedObject(); variable["name"] = "sensor"; variable["type"] = "number"; variable["readable"] = true;
  JsonObject function = doc["functions"].createNestedObject(); function["name"] = "set_led";
  JsonObject led = doc["pins"].createNestedObject(); led["name"] = "status_led"; led["number"] = LED_PIN; led["type"] = "digital"; led["writable"] = true; led["modes"].add("input"); led["modes"].add("output");
  JsonObject sensor = doc["pins"].createNestedObject(); sensor["name"] = "sensor"; sensor["number"] = SENSOR_PIN; sensor["type"] = "analog"; sensor["writable"] = false; sensor["modes"].add("input");
  char payload[768]; serializeJson(doc, payload, sizeof(payload)); mqtt.publish(topic("manifest").c_str(), payload, true);
}

void connectMQTT() {
  while (!mqtt.connected()) {
    String clientId = String("apteva-") + DEVICE_ID;
    if (mqtt.connect(clientId.c_str(), DEVICE_ID, DEVICE_PASSWORD, topic("availability").c_str(), 1, true, "offline")) {
      mqtt.subscribe(topic("commands").c_str(), 1); mqtt.publish(topic("availability").c_str(), "online", true); publishManifest();
    } else delay(2000);
  }
}

void setup() {
  pinMode(LED_PIN, OUTPUT); pinMode(SENSOR_PIN, INPUT);
  WiFi.begin(WIFI_SSID, WIFI_PASSWORD); while (WiFi.status() != WL_CONNECTED) delay(250);
  mqtt.setServer(MQTT_HOST, MQTT_PORT); mqtt.setCallback(handleCommand); mqtt.setBufferSize(1536); connectMQTT();
}

void loop() {
  if (!mqtt.connected()) connectMQTT(); mqtt.loop();
  if (millis() - lastTelemetry >= 10000) {
    lastTelemetry = millis(); StaticJsonDocument<128> data; data["sensor"] = analogRead(SENSOR_PIN); data["uptime_ms"] = millis();
    char payload[128]; serializeJson(data, payload, sizeof(payload)); mqtt.publish(topic("telemetry").c_str(), payload, false);
  }
}
