# kotlinx.serialization keep rules
-keepattributes *Annotation*, InnerClasses
-dontnote kotlinx.serialization.AnnotationsKt

-keep,includedescriptorclasses class com.desklink.client.**$$serializer { *; }
-keepclassmembers class com.desklink.client.** {
    *** Companion;
}
-keepclasseswithmembers class com.desklink.client.** {
    kotlinx.serialization.KSerializer serializer(...);
}

# ML Kit (QR scanner) creates its component registrars by name from manifest
# metadata. R8 full mode keeps the class names from ML Kit's own rules but
# drops the constructors and getComponents(), so the registry comes up empty
# and the scanner throws a NullPointerException.
-keep class * implements com.google.firebase.components.ComponentRegistrar {
    <init>();
    java.util.List getComponents();
}
