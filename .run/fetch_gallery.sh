#!/bin/bash
export SDRSTREAM_SATNOGS_PROXY='https://api.allorigins.win/raw?url={url}'
for i in $(seq 1 40); do
  out=$(/tmp/sdrstream-web gallery -days 3 -limit 20 -max-mb 8 2>&1)
  if ! printf '%s' "$out" | grep -q '^error'; then
    printf '%s\n' "$out" > /root/test/sdrstream/.run/gallery.out
    echo "SUCCESS on attempt $i" >> /root/test/sdrstream/.run/gallery.status
    exit 0
  fi
  echo "attempt $i failed: $(printf '%s' "$out" | tail -1)" >> /root/test/sdrstream/.run/gallery.status
  sleep 15
done
echo "gave up" >> /root/test/sdrstream/.run/gallery.status
