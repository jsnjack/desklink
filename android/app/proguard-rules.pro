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
