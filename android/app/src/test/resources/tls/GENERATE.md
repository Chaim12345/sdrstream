Test fixtures ONLY: throwaway self-signed certificates for SmtpClientTest's
TLS hostname-verification tests. Not credentials; the passwords are in this
command. Regenerate with:

  keytool -genkeypair -alias localhost -keyalg RSA -keysize 2048 -validity 3650 \
    -dname "CN=localhost" -ext "SAN=dns:localhost" \
    -keystore localhost.p12 -storepass testonly -keypass testonly -storetype PKCS12
  keytool -genkeypair -alias wronghost -keyalg RSA -keysize 2048 -validity 3650 \
    -dname "CN=wrong-host.example" -ext "SAN=dns:wrong-host.example" \
    -keystore wronghost.p12 -storepass testonly -keypass testonly -storetype PKCS12
