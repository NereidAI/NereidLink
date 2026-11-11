-- Copyright (C) 2026 YIQISOFT
-- SPDX-License-Identifier: Apache-2.0

-- 0063_protocol_catalog.up.sql introduces the global, operator-editable
-- protocol/device-service catalog (ADR-0033). Before this migration the
-- console's knowledge of the EdgeX reference device services (the fields an
-- edge expects in Device.Protocols and in per-resource attributes) lived in a
-- hardcoded frontend module, so it was neither enumerable nor maintainable in
-- the UI. It now lives here as managed data.
--
-- Scope and invariants:
--   * The catalog is GLOBAL (no tenant_id): device-service wire facts are the
--     same vendor facts for every tenant. The tenant-scoped REST path only
--     supplies authorization. The kernel itself still parses no protocol;
--     these rows drive UI forms and suggestions, never ingestion.
--   * builtin=true rows are the fourteen device services documented by the
--     EdgeX 4.0.2 device-service index. They may be edited (corrections) but
--     must not be deleted (the service layer rejects the delete); custom
--     operator families use builtin=false and are fully removable.
--   * connection_fields/point_fields are opaque JSONB field descriptors
--     ({key,label,kind,required,placeholder,options,transports,...}); the
--     console renders them and emits `key` verbatim into the protocol/address
--     JSON the edge consumes.

CREATE TABLE IF NOT EXISTS protocol_catalog (
    family        TEXT PRIMARY KEY,
    display_name  TEXT NOT NULL,
    service_name  TEXT NOT NULL DEFAULT '',
    protocol_key  TEXT NOT NULL DEFAULT '',
    transports    JSONB NOT NULL DEFAULT '[]'::jsonb,
    connection_fields JSONB NOT NULL DEFAULT '[]'::jsonb,
    point_fields  JSONB NOT NULL DEFAULT '[]'::jsonb,
    default_point_address JSONB NOT NULL DEFAULT '{}'::jsonb,
    doc_url       TEXT NOT NULL DEFAULT '',
    builtin       BOOLEAN NOT NULL DEFAULT TRUE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS protocol_catalog_service_idx
    ON protocol_catalog (service_name);

-- Seed the fourteen official EdgeX 4.0.2 device services. Field keys match
-- the exact property casing each reference device service reads.

INSERT INTO protocol_catalog
(family, display_name, service_name, protocol_key, transports,
 connection_fields, point_fields, default_point_address, doc_url, builtin)
VALUES
-- Modbus (device-modbus): TCP/RTU/ASCII transports.
('modbus', 'Modbus', 'device-modbus', 'modbus-tcp',
 '[{"value":"modbus-tcp","label":"Modbus TCP"},{"value":"modbus-rtu","label":"Modbus RTU"},{"value":"modbus-ascii","label":"Modbus ASCII"}]'::jsonb,
 '[
  {"key":"Address","label":"Address","kind":"string","required":true,"placeholder":"192.0.2.10","transports":["*"]},
  {"key":"Port","label":"Port","kind":"integer","required":true,"placeholder":"502","min":1,"max":65535,"transports":["*"]},
  {"key":"UnitID","label":"Unit ID","kind":"integer","required":true,"placeholder":"1","min":0,"max":247,"transports":["*"]},
  {"key":"Timeout","label":"Timeout","kind":"duration","placeholder":"5s","transports":["*"]},
  {"key":"IdleTimeout","label":"Idle timeout","kind":"duration","placeholder":"1m","transports":["*"]},
  {"key":"BaudRate","label":"Baud rate","kind":"integer","required":true,"placeholder":"9600","transports":["modbus-rtu","modbus-ascii"]},
  {"key":"DataBits","label":"Data bits","kind":"integer","placeholder":"8","options":[{"value":"7","label":"7"},{"value":"8","label":"8"}],"transports":["modbus-rtu","modbus-ascii"]},
  {"key":"StopBits","label":"Stop bits","kind":"integer","placeholder":"1","options":[{"value":"1","label":"1"},{"value":"2","label":"2"}],"transports":["modbus-rtu","modbus-ascii"]},
  {"key":"Parity","label":"Parity","kind":"select","options":[{"value":"N","label":"N"},{"value":"O","label":"O"},{"value":"E","label":"E"}],"transports":["modbus-rtu","modbus-ascii"]},
  {"key":"ConnectDelay","label":"Connect delay","kind":"duration","placeholder":"500ms","transports":["modbus-tcp"]},
  {"key":"ProtocolRecoveryTimeout","label":"Protocol recovery timeout","kind":"duration","placeholder":"0s","transports":["modbus-tcp"]},
  {"key":"LinkRecoveryTimeout","label":"Link recovery timeout","kind":"duration","placeholder":"0s","transports":["modbus-tcp"]}
 ]'::jsonb,
 '[
  {"key":"primaryTable","label":"Primary table","kind":"select","required":true,"defaultValue":"HOLDING_REGISTERS","options":[{"value":"COILS","label":"COILS"},{"value":"DISCRETE_INPUTS","label":"DISCRETE_INPUTS"},{"value":"DISCRETES_INPUT","label":"DISCRETES_INPUT"},{"value":"HOLDING_REGISTERS","label":"HOLDING_REGISTERS"},{"value":"INPUT_REGISTERS","label":"INPUT_REGISTERS"}]},
  {"key":"startingAddress","label":"Starting address","kind":"integer","required":true,"placeholder":"0","min":0,"defaultValue":0},
  {"key":"isByteSwap","label":"Byte swap","kind":"boolean","defaultValue":false},
  {"key":"isWordSwap","label":"Word swap","kind":"boolean","defaultValue":false},
  {"key":"rawType","label":"Raw type","kind":"select","options":[{"value":"INT16","label":"INT16"},{"value":"UINT16","label":"UINT16"},{"value":"INT32","label":"INT32"},{"value":"UINT32","label":"UINT32"},{"value":"INT64","label":"INT64"},{"value":"UINT64","label":"UINT64"},{"value":"FLOAT32","label":"FLOAT32"},{"value":"FLOAT64","label":"FLOAT64"},{"value":"BOOL","label":"BOOL"}]},
  {"key":"stringRegisterSize","label":"String register size","kind":"integer","placeholder":"2","min":1}
 ]'::jsonb,
 '{"primaryTable":"HOLDING_REGISTERS","startingAddress":0}'::jsonb,
 'https://docs.edgexfoundry.org/4.0.2/microservices/device/services/device-modbus/ProtocolProperties/', TRUE),

-- ONVIF camera (device-onvif-camera).
('onvif', 'ONVIF camera', 'device-onvif-camera', 'Onvif',
 '[{"value":"Onvif","label":"ONVIF"}]'::jsonb,
 '[
  {"key":"Address","label":"Address","kind":"string","required":true,"placeholder":"192.0.2.64","transports":["*"]},
  {"key":"Port","label":"Port","kind":"integer","placeholder":"80","min":1,"max":65535,"transports":["*"]},
  {"key":"MACAddress","label":"MAC address","kind":"string","placeholder":"AA:BB:CC:DD:EE:FF","transports":["*"]},
  {"key":"FriendlyName","label":"Friendly name","kind":"string","placeholder":"front-door","transports":["*"]},
  {"key":"EndpointRefAddress","label":"EndpointRef address","kind":"string","placeholder":"urn:uuid:...","transports":["*"]}
 ]'::jsonb,
 '[]'::jsonb, '{}'::jsonb,
 'https://docs.edgexfoundry.org/4.0.2/microservices/device/services/device-onvif-camera/General/', TRUE),

-- MQTT (device-mqtt): the service reads only CommandTopic from properties.
('mqtt', 'MQTT', 'device-mqtt', 'mqtt',
 '[{"value":"mqtt","label":"MQTT"}]'::jsonb,
 '[
  {"key":"CommandTopic","label":"Command topic","kind":"string","required":true,"placeholder":"command/example-device","transports":["*"]}
 ]'::jsonb,
 '[]'::jsonb, '{}'::jsonb,
 'https://docs.edgexfoundry.org/4.0.2/microservices/device/services/device-mqtt/ProtocolProperties/', TRUE),

-- REST (device-rest): key "REST" with Host/Port/Path.
('rest', 'REST', 'device-rest', 'REST',
 '[{"value":"REST","label":"REST"}]'::jsonb,
 '[
  {"key":"Host","label":"Host","kind":"string","required":true,"placeholder":"127.0.0.1","transports":["*"]},
  {"key":"Port","label":"Port","kind":"integer","required":true,"placeholder":"5000","min":1,"max":65535,"transports":["*"]},
  {"key":"Path","label":"Path","kind":"string","placeholder":"api","transports":["*"]},
  {"key":"urlRawQuery","label":"URL raw query","kind":"string","placeholder":"ts=1","transports":["*"]}
 ]'::jsonb,
 '[]'::jsonb, '{}'::jsonb,
 'https://docs.edgexfoundry.org/4.0.2/microservices/device/services/device-rest/ProtocolProperties/', TRUE),

-- Device virtual: placeholder key "other".
('virtual', 'Device virtual', 'device-virtual', 'other',
 '[{"value":"other","label":"Other"}]'::jsonb,
 '[
  {"key":"Address","label":"Address","kind":"string","placeholder":"device-virtual-float-01","transports":["*"]},
  {"key":"Port","label":"Port","kind":"integer","placeholder":"300","min":1,"max":65535,"transports":["*"]}
 ]'::jsonb,
 '[]'::jsonb, '{}'::jsonb,
 'https://docs.edgexfoundry.org/4.0.2/microservices/device/services/device-virtual/Ch-VirtualDevice/', TRUE),

-- SNMP (device-snmp): key "TCP" with Address/Port.
('snmp', 'SNMP', 'device-snmp', 'TCP',
 '[{"value":"TCP","label":"TCP"}]'::jsonb,
 '[
  {"key":"Address","label":"Address","kind":"string","required":true,"placeholder":"192.0.2.200","transports":["*"]},
  {"key":"Port","label":"Port","kind":"integer","required":true,"placeholder":"161","min":1,"max":65535,"transports":["*"]}
 ]'::jsonb,
 '[]'::jsonb, '{}'::jsonb,
 'https://docs.edgexfoundry.org/4.0.2/microservices/device/services/device-snmp/', TRUE),

-- BACnet (device-bacnet-c): IP and MSTP transports.
('bacnet', 'BACnet', 'device-bacnet-ip', 'BACnet-IP',
 '[{"value":"BACnet-IP","label":"BACnet/IP"},{"value":"BACnet-MSTP","label":"BACnet MS/TP"}]'::jsonb,
 '[
  {"key":"DeviceInstance","label":"Device instance","kind":"string","required":true,"placeholder":"52","transports":["*"]},
  {"key":"Port","label":"Port","kind":"integer","placeholder":"47808","min":1,"max":65535,"transports":["BACnet-IP"]},
  {"key":"MAC","label":"MAC","kind":"string","placeholder":"5","transports":["BACnet-MSTP"]}
 ]'::jsonb,
 '[]'::jsonb, '{}'::jsonb,
 'https://docs.edgexfoundry.org/4.0.2/microservices/device/services/device-bacnet/', TRUE),

-- CoAP (device-coap-c): ED_-prefixed properties.
('coap', 'CoAP', 'device-coap', 'COAP',
 '[{"value":"COAP","label":"CoAP"}]'::jsonb,
 '[
  {"key":"ED_ADDR","label":"Endpoint address","kind":"string","required":true,"placeholder":"127.0.0.1","transports":["*"]},
  {"key":"ED_SecurityMode","label":"Security mode","kind":"select","options":[{"value":"NoSec","label":"NoSec"},{"value":"PSK","label":"PSK"}],"transports":["*"]},
  {"key":"ED_PskKey","label":"PSK key","kind":"string","placeholder":"••••••","transports":["*"]}
 ]'::jsonb,
 '[]'::jsonb, '{}'::jsonb,
 'https://docs.edgexfoundry.org/4.0.2/microservices/device/services/device-coap/', TRUE),

-- GPIO (device-gpio): device properties ignored; addressing is per-resource.
('gpio', 'GPIO', 'device-gpio', '',
 '[]'::jsonb, '[]'::jsonb, '[]'::jsonb, '{}'::jsonb,
 'https://docs.edgexfoundry.org/4.0.2/microservices/device/services/device-gpio/ProtocolProperties/', TRUE),

-- OPC-UA (device-opc-ua): Endpoint per device, nodeId per resource.
('opcua', 'OPC-UA', 'device-opc-ua', 'opcua',
 '[{"value":"opcua","label":"OPC-UA"}]'::jsonb,
 '[
  {"key":"Endpoint","label":"Endpoint","kind":"string","required":true,"placeholder":"opc.tcp://host:53530/OPCUA/SimulationServer","transports":["*"]}
 ]'::jsonb,
 '[
  {"key":"nodeId","label":"Node ID","kind":"string","required":true,"placeholder":"ns=3;s=Counter"}
 ]'::jsonb,
 '{}'::jsonb,
 'https://docs.edgexfoundry.org/4.0.2/microservices/device/services/device-opc-ua/ProtocolProperties/', TRUE),

-- RFID LLRP (device-rfid-llrp): discovery-driven; no fixed property template.
('llrp', 'RFID LLRP', 'device-rfid-llrp', 'llrp',
 '[{"value":"llrp","label":"LLRP"}]'::jsonb,
 '[]'::jsonb, '[]'::jsonb, '{}'::jsonb,
 'https://docs.edgexfoundry.org/4.0.2/microservices/device/services/device-rfid-llrp/', TRUE),

-- S7 (device-s7): ISO-on-TCP; Timeout/IdleTimeout are INTEGER SECONDS and the
-- only point-address attribute is NodeName (e.g. "DB4.DBX0.0").
('s7', 'Siemens S7', 'device-s7', 's7',
 '[{"value":"s7","label":"S7 (ISO-on-TCP)"}]'::jsonb,
 '[
  {"key":"Host","label":"Host","kind":"string","required":true,"placeholder":"192.0.2.199","transports":["*"]},
  {"key":"Port","label":"Port","kind":"integer","required":true,"placeholder":"102","min":1,"max":65535,"transports":["*"]},
  {"key":"Rack","label":"Rack","kind":"integer","required":true,"placeholder":"0","transports":["*"]},
  {"key":"Slot","label":"Slot","kind":"integer","required":true,"placeholder":"1","transports":["*"]},
  {"key":"Timeout","label":"Connect timeout (seconds)","kind":"integer","placeholder":"30","transports":["*"]},
  {"key":"IdleTimeout","label":"Idle timeout (seconds)","kind":"integer","placeholder":"30","transports":["*"]}
 ]'::jsonb,
 '[
  {"key":"NodeName","label":"Node name","kind":"string","required":true,"placeholder":"DB4.DBX0.0"}
 ]'::jsonb,
 '{}'::jsonb,
 'https://docs.edgexfoundry.org/4.0.2/microservices/device/services/device-s7/ProtocolProperties/', TRUE),

-- UART (device-uart): UART key with deviceLocation/baudRate.
('uart', 'UART (serial)', 'device-uart', 'UART',
 '[{"value":"UART","label":"UART"}]'::jsonb,
 '[
  {"key":"deviceLocation","label":"Device location","kind":"string","required":true,"placeholder":"/dev/ttyAMA2","transports":["*"]},
  {"key":"baudRate","label":"Baud rate","kind":"integer","required":true,"placeholder":"115200","transports":["*"]}
 ]'::jsonb,
 '[]'::jsonb, '{}'::jsonb,
 'https://docs.edgexfoundry.org/4.0.2/microservices/device/services/device-uart/ProtocolProperties/', TRUE),

-- USB camera (device-usb-camera): discovery-driven; no fixed property template.
('usb-camera', 'USB camera', 'device-usb-camera', '',
 '[]'::jsonb, '[]'::jsonb, '[]'::jsonb, '{}'::jsonb,
 'https://docs.edgexfoundry.org/4.0.2/microservices/device/services/device-usb-camera/General/', TRUE)

ON CONFLICT (family) DO NOTHING;
