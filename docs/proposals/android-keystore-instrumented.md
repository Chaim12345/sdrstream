# Proposal (NOT enabled): Keystore instrumented round trip

Status: review-only patch. Nothing here is under .github/workflows, so it triggers nothing.

1. android/app/build.gradle additions (not applied):
   defaultConfig { testInstrumentationRunner "androidx.test.runner.AndroidJUnitRunner" }
   dependencies { androidTestImplementation 'androidx.test.ext:junit:1.1.5'
                  androidTestImplementation 'androidx.test:core:1.5.0' }
2. Test source: android/app/src/androidTest/.../SecretStoreInstrumentedTest.java (dummy values, test-only pref key, cleanup in @After, no consent flags changed).
3. Workflow shape, if the user approves: a NEW file with `on: workflow_dispatch` only, one job: KVM enable, then reactivecircus/android-emulator-runner@v2 (api-level 34, google_apis, x86_64) running `cd android && ./gradlew connectedDebugAndroidTest`. Also add `./gradlew testDebugUnitTest` to android.yml so JVM tests run at all.
4. Caveat: x86_64 emulator Keystore is software-backed; it proves API acceptance of the IV policy, not hardware backing.
