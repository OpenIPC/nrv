package com.open_nvr

import com.facebook.react.ReactPackage
import com.facebook.react.bridge.NativeModule
import com.facebook.react.bridge.ReactApplicationContext
import com.facebook.react.uimanager.ViewManager

/**
 * Регистрация модуля показа тревог.
 *
 * Автоподключение ищет библиотеки только в node_modules, поэтому собственный
 * модуль приложения регистрируется вручную — см. MainApplication.
 */
class PushNotifyPackage : ReactPackage {

    override fun createNativeModules(
        reactContext: ReactApplicationContext,
    ): List<NativeModule> = listOf(PushNotifyModule(reactContext))

    override fun createViewManagers(
        reactContext: ReactApplicationContext,
    ): List<ViewManager<*, *>> = emptyList()
}
