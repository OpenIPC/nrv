package com.mobile

import android.content.Context
import android.media.AudioManager
import android.media.Ringtone
import android.media.RingtoneManager
import com.facebook.react.bridge.ReactApplicationContext
import com.facebook.react.bridge.ReactContextBaseJavaModule
import com.facebook.react.bridge.ReactMethod

/**
 * Звук звонка: громкая связь и рингтон.
 *
 * Свой модуль вместо сторонней библиотеки. Причина — падение, найденное на
 * живом телефоне: InCallManager регистрировал приёмник нажатий кнопок
 * гарнитуры без обязательных на Android 14 флагов получателя
 * (`RECEIVER_EXPORTED`/`RECEIVER_NOT_EXPORTED`), система обрывала приложение
 * прямо во время звонка. Из всей библиотеки нам нужны две вещи, и их проще
 * сделать здесь, чем ждать исправления в чужом коде.
 *
 * Режим звука (MODE_IN_COMMUNICATION) не трогаем: его выставляет сам WebRTC,
 * когда поднимает аудиодорожку, и спор с ним приводил бы к «то слышно, то нет».
 */
class CallAudioModule(reactContext: ReactApplicationContext) :
    ReactContextBaseJavaModule(reactContext) {

    private var ringtone: Ringtone? = null

    override fun getName(): String = "NvrCallAudio"

    private fun audioManager(): AudioManager =
        reactApplicationContext.getSystemService(Context.AUDIO_SERVICE) as AudioManager

    /**
     * Включает и выключает громкую связь.
     *
     * У домофона это не удобство, а необходимость: человека у калитки слышно
     * только через громкий динамик, а разговорный рассчитан на телефон у уха.
     */
    @ReactMethod
    fun setSpeaker(on: Boolean) {
        try {
            val manager = audioManager()
            // Режим выставляем перед переключением: на части прошивок
            // динамик не переключается, пока звук идёт в режиме звонка.
            manager.mode = AudioManager.MODE_IN_COMMUNICATION
            manager.isSpeakerphoneOn = on
        } catch (error: Exception) {
            // Отказ прошивки не должен ронять приложение: разговор важнее
            // того, куда именно идёт звук.
        }
    }

    /** Включает и выключает микрофон. */
    @ReactMethod
    fun setMute(muted: Boolean) {
        try {
            audioManager().isMicrophoneMute = muted
        } catch (error: Exception) {
            // Тот же случай: молчание лучше падения.
        }
    }

    /** Играет системный рингтон при входящем вызове. */
    @ReactMethod
    fun startRingtone() {
        try {
            stopRingtone()
            val uri = RingtoneManager.getDefaultUri(RingtoneManager.TYPE_RINGTONE)
            ringtone = RingtoneManager.getRingtone(reactApplicationContext, uri)
            ringtone?.play()
        } catch (error: Exception) {
            // На части прошивок рингтон недоступен — вызов всё равно покажется
            // на экране, и этого достаточно, чтобы его не пропустить.
        }
    }

    /** Останавливает рингтон. */
    @ReactMethod
    fun stopRingtone() {
        try {
            ringtone?.stop()
        } catch (error: Exception) {
            // Уже не играет.
        }
        ringtone = null
    }
}
