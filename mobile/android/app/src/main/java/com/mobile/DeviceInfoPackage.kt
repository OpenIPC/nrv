package com.mobile

import com.facebook.react.ReactPackage
import com.facebook.react.bridge.NativeModule
import com.facebook.react.bridge.ReactApplicationContext
import com.facebook.react.uimanager.ViewManager

/**
 * Регистрация собственных нативных модулей приложения.
 *
 * Автоподключение (autolinking) работает только для сторонних библиотек
 * из node_modules, поэтому модуль, написанный для этого приложения,
 * нужно добавить вручную — см. MainApplication.
 */
class DeviceInfoPackage : ReactPackage {

    override fun createNativeModules(
        reactContext: ReactApplicationContext,
    ): List<NativeModule> = listOf(DeviceInfoModule(reactContext))

    override fun createViewManagers(
        reactContext: ReactApplicationContext,
    ): List<ViewManager<*, *>> = emptyList()
}
