package com.mobile

import android.content.Context
import android.content.Intent
import android.net.Uri
import android.os.PowerManager
import android.provider.Settings
import com.facebook.react.bridge.Promise
import com.facebook.react.bridge.ReactApplicationContext
import com.facebook.react.bridge.ReactContextBaseJavaModule
import com.facebook.react.bridge.ReactMethod

/**
 * Управление службой переднего плана из JavaScript.
 *
 * Служба нужна, пока линия зарегистрирована: без неё Android закрывает
 * приложение в фоне и входящий звонок не приходит. Запускает и останавливает
 * её JavaScript — он один знает, есть ли линия и вошёл ли пользователь.
 *
 * Имя модуля с префиксом приложения: в React Native уже есть служебные
 * модули, и совпадение имён не даёт приложению запуститься.
 */
class SipForegroundModule(reactContext: ReactApplicationContext) :
    ReactContextBaseJavaModule(reactContext) {

    override fun getName(): String = "NvrSipForeground"

    /** Поднимает службу с постоянным уведомлением. */
    @ReactMethod
    fun start(title: String, text: String) {
        SipForegroundService.start(reactApplicationContext, title, text)
    }

    /** Меняет текст уведомления: например, «звонок с 101». */
    @ReactMethod
    fun update(title: String, text: String) {
        SipForegroundService.update(reactApplicationContext, title, text)
    }

    /** Останавливает службу: линия больше не нужна. */
    @ReactMethod
    fun stop() {
        SipForegroundService.stop(reactApplicationContext)
    }

    /**
     * Просит систему не «гибернировать» приложение.
     *
     * Проверено на телефоне TECNO: прошивка (Usf_Hiber) замораживает процесс
     * через несколько секунд после ухода в фон, и вызов перестаёт доходить,
     * несмотря на службу переднего плана и блокировки. Исключение из
     * оптимизации батареи выводит приложение из-под этого механизма.
     *
     * Возвращает true, если исключение уже выдано: тогда диалог не показываем.
     */
    @ReactMethod
    fun requestBatteryExemption(promise: Promise) {
        try {
            val power = reactApplicationContext.getSystemService(Context.POWER_SERVICE)
                as PowerManager
            val packageName = reactApplicationContext.packageName
            if (power.isIgnoringBatteryOptimizations(packageName)) {
                promise.resolve(true)
                return
            }
            val intent = Intent(Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS)
                .setData(Uri.parse("package:$packageName"))
                .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
            reactApplicationContext.startActivity(intent)
            promise.resolve(false)
        } catch (error: Exception) {
            // Прошивка может не знать такого действия — тогда остаются
            // ручные настройки, о которых скажет интерфейс.
            promise.reject("battery_exemption_failed", error)
        }
    }

    /** Сообщает, выдано ли уже исключение по батарее. */
    @ReactMethod
    fun isBatteryExempt(promise: Promise) {
        try {
            val power = reactApplicationContext.getSystemService(Context.POWER_SERVICE)
                as PowerManager
            promise.resolve(power.isIgnoringBatteryOptimizations(reactApplicationContext.packageName))
        } catch (error: Exception) {
            promise.reject("battery_state_failed", error)
        }
    }

    /** Показывает входящий вызов на весь экран поверх блокировки. */
    @ReactMethod
    fun incomingCall(title: String, text: String) {
        SipForegroundService.incoming(reactApplicationContext, title, text)
    }

    /** Убирает полноэкранное уведомление: вызов принят или завершён. */
    @ReactMethod
    fun clearIncomingCall() {
        SipForegroundService.clearIncoming(reactApplicationContext)
    }

    /**
     * Показывает экран приложения поверх блокировки.
     *
     * Нужно для входящего вызова: если телефон лежит с погашенным экраном,
     * без этого оператор увидит только уведомление и может не успеть ответить.
     * Активность сама включает показ поверх блокировки и подсветку экрана
     * (см. MainActivity).
     */
    @ReactMethod
    fun wake() {
        try {
            val intent = reactApplicationContext.packageManager
                .getLaunchIntentForPackage(reactApplicationContext.packageName)
                ?.apply {
                    addFlags(
                        android.content.Intent.FLAG_ACTIVITY_NEW_TASK or
                            android.content.Intent.FLAG_ACTIVITY_SINGLE_TOP,
                    )
                }
            if (intent != null) {
                reactApplicationContext.startActivity(intent)
            }
        } catch (error: Exception) {
            // Система может запретить запуск из фона — тогда вызов остаётся
            // виден в уведомлении, и ответить можно оттуда.
        }
    }
}
