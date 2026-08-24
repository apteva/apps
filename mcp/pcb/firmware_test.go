package main

import (
	"reflect"
	"testing"
)

func TestArduinoFirmwareLabSensorTutorial(t *testing.T) {
	def := sensorNodeExample()
	source := `
#include <Wire.h>
void setup() {
  Serial.begin(115200);
  Wire.begin();
  pinMode(8, OUTPUT);
  Wire.beginTransmission(0x70);
  Wire.write(0x7C);
  Wire.endTransmission();
}
void loop() {
  float temperature = sensor.readTemperature();
  float humidity = sensor.readHumidity();
  Serial.print("Temperature: ");
  Serial.println(temperature);
  Serial.print("Humidity: ");
  Serial.println(humidity);
  digitalWrite(8, HIGH);
  delay(1000);
}`
	result, err := runFirmwareLab(&def, FirmwareOptions{Source: source, Iterations: 2})
	if err != nil {
		t.Fatal(err)
	}
	if result.Board != "esp32c3" || result.SerialBaud != 115200 || result.DurationUS != 2_000_000 {
		t.Fatalf("runtime metadata: %#v", result)
	}
	wantSerial := []string{"Temperature: 23.5", "Humidity: 45", "Temperature: 23.5", "Humidity: 45"}
	if !reflect.DeepEqual(result.SerialOutput, wantSerial) {
		t.Fatalf("serial=%v want %v", result.SerialOutput, wantSerial)
	}
	if result.PinStates["8"] != "high" || len(result.I2CTransactions) != 1 || result.I2CTransactions[0].Status != "ack" {
		t.Fatalf("virtual IO missing: pins=%v i2c=%v", result.PinStates, result.I2CTransactions)
	}
}

func TestArduinoFirmwareLabRejectsUnboundedRuns(t *testing.T) {
	def := emptyDefinition("firmware")
	if _, err := runFirmwareLab(&def, FirmwareOptions{Source: `void loop(){ delay(1); }`, Iterations: 101}); err == nil {
		t.Fatal("expected iteration ceiling")
	}
}
