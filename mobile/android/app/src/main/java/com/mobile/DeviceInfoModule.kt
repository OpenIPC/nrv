package com.mobile

import android.app.UiModeManager
import android.content.Context
import android.content.res.Configuration
import com.facebook.react.bridge.Promise
import com.facebook.react.bridge.ReactApplicationContext
import com.facebook.react.bridge.ReactContextBaseJavaModule
import com.facebook.react.bridge.ReactMethod

/**
 * Сведения об устройстве, которых нет в JavaScript.
 *
 * Отдельный модуль нужен ради одной проверки: телевизор это или телефон.
 * Из JavaScript её сделать нельзя — в React Native нет доступа к режиму
 * интерфейса системы.
 */
class DeviceInfoModule(reactContext: ReactApplicationContext) :
    ReactContextBaseJavaModule(reactContext) {

    override fun getName(): String = "DeviceInfo"

    /**
     * Сообщает, что приложение запущено на телевизоре.
     *
     * Признак — режим интерфейса Leanback: так система помечает Android TV,
     * приставки и телевизоры со встроенным Android. Определять по размеру
     * экрана нельзя: планшет тоже большой, но управляется касаниями,
     * и интерфейс для пульта на нём был бы неудобен.
     */
    @ReactMethod
    fun isTelevision(promise: Promise) {
        try {
            val uiModeManager =
                reactApplicationContext.getSystemService(Context.UI_MODE_SERVICE) as UiModeManager

            val television = when (uiModeManager.currentModeType) {
                // READY — это телевизор, CARDOCK — док-станция,
                // TERTIARY — автомобильная система. Пульт есть только у первого.
                // Сами константы режимов объявлены в Configuration,
                // у UiModeManager их нет.
                Configuration.UI_MODE_TYPE_TELEVISION -> true
                else -> false
            }

            promise.resolve(television)
        } catch (error: Exception) {
            // Система может не отдать службу: тогда честно сообщаем об
            // ошибке, а сторона JavaScript решит считать устройство телефоном.
            promise.reject("device_info_failed", error)
        }
    }
}
