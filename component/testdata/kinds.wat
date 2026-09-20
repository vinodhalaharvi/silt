;; Every kind of top-level import, so the reader is tested against what the
;; encoder actually emits rather than against bytes written by hand. The
;; nested component's import must not be reported: it is satisfied inside
;; the binary and reaches nothing on the host.
(component
  (import "silt:modbus/read@0.1.0" (instance
    (export "read-holding" (func (param "addr" u16) (result u16)))))
  (import "log" (func))
  (import "handle" (type (sub resource)))
  (import "silt:plugin/driver" (component))
  (import "blob" (core module))
  (import "wasi:clocks/monotonic-clock@0.3.0" (instance))
  (component $inner
    (import "must-not-appear" (func)))
)
