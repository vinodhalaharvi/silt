;; A fake Modbus device exporting silt:modbus/read: register N holds N.
(component
  (core module $m
    (memory (export "mem") 1)
    ;; Canonical ABI: result<list<u16>, string> is returned through a pointer
    ;; to [disc:u8 @0, list ptr:i32 @4, list len:i32 @8].
    (func (export "read") (param $unit i32) (param $addr i32) (param $count i32) (result i32)
      (local $i i32)
      (block $done
        (loop $fill
          (br_if $done (i32.ge_u (local.get $i) (local.get $count)))
          (i32.store16
            (i32.add (i32.const 64) (i32.shl (local.get $i) (i32.const 1)))
            (i32.add (local.get $addr) (local.get $i)))
          (local.set $i (i32.add (local.get $i) (i32.const 1)))
          (br $fill)))
      (i32.store8 (i32.const 0) (i32.const 0))
      (i32.store (i32.const 4) (i32.const 64))
      (i32.store (i32.const 8) (local.get $count))
      (i32.const 0)))
  (core instance $i (instantiate $m))
  (func $read (param "unit" u8) (param "addr" u16) (param "count" u16)
    (result (result (list u16) (error string)))
    (canon lift (core func $i "read") (memory (core memory $i "mem"))))
  (instance $read-inst (export "read-holding" (func $read)))
  (export "silt:modbus/read@0.1.0" (instance $read-inst))
)
