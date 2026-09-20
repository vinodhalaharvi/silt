;; An MCP gateway that reads registers, browses OPC UA, and serves HTTP.
(component
  (import "silt:modbus/read@0.1.0" (instance))
  (import "silt:opcua/browse@0.1.0" (instance))
  (import "wasi:http/incoming-handler@0.3.0" (instance))
)
