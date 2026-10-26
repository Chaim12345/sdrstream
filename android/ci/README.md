# Dormant storage integration test support

SecretStore, SecretCrypto, existing exact-secret redaction support and tests were selected byte-for-byte from reviewed Android candidate42692a74c13fdf7e9d4cfa3b3215cdbd9c576250. This integration intentionally excludes its key menus, provider environment injection and consent preference methods. No existing feature was removed from that candidate branch. Its full source and history remain preserved independently.

This is a new integration tree, not the candidate's exact-SHA runtime result. No Android build, emulator or phone execution of this integration is implied by the candidate's earlier pass. The additional SecretValueRedactionTest uses dummy data and tests the existing registry dependency.

No Actions workflow is imported here. For explicit test execution in an Android SDK environment:

    cd android
    ./gradlew testDebugUnitTest -PsdrstreamAbis=x86_64
    ./gradlew connectedDebugAndroidTest -PsdrstreamAbis=x86_64 -Pandroid.useAndroidX=true --no-daemon

The AndroidX flag is test-scoped; shipping gradle.properties remains unchanged. On the fixed code all five instrumented tests must execute without failures/skips. From repository root, the strict XML checker can validate:

    python3 android/ci/keystore_verdict.py fixed 0

Record the actual Gradle exit status, not a fabricated0. Baseline comparison requires the separately preserved reviewed candidate workflow/history. No scheduler, provider activation, key collection, APK publication or phone effects are enabled by this directory.
