package com.open_nvr

import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.os.Build
import androidx.core.app.NotificationCompat
import com.facebook.react.bridge.ReactApplicationContext
import com.facebook.react.bridge.ReactContextBaseJavaModule
import com.facebook.react.bridge.ReactMethod

/**
 * Показ уведомления о тревоге, когда приложение открыто.
 *
 * В свёрнутом и закрытом приложении уведомления показывает сама система: они
 * приходят от Firebase как notification-сообщения, и это её работа. На
 * переднем плане система их намеренно не показывает — иначе тревога всплывала
 * бы поверх приложения, которое и так открыто. Но совсем молчать нельзя:
 * человек смотрит камеру и не узнает о срабатывании. Поэтому показываем сами,
 * тем же способом и каналом, что и система.
 *
 * Имя модуля с префиксом приложения: служебные модули React Native носят
 * простые имена, и совпадение не даёт приложению запуститься.
 */
class PushNotifyModule(reactContext: ReactApplicationContext) :
    ReactContextBaseJavaModule(reactContext) {

    companion object {
        private const val CHANNEL_ID = "nvr_alerts"

        /**
         * Отдельный канал для тревог, а не канал линии.
         *
         * У линии важность LOW — её уведомление висит постоянно, и звук при
         * каждом входе в приложение раздражал бы. Тревога, наоборот, должна
         * звонить: у неё высокий приоритет и, соответственно, отдельный канал.
         */
        private fun createChannel(context: Context) {
            if (Build.VERSION.SDK_INT < Build.VERSION_CODES.O) return
            val manager =
                context.getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager
            if (manager.getNotificationChannel(CHANNEL_ID) != null) return
            val channel = NotificationChannel(
                CHANNEL_ID,
                "Тревоги",
                NotificationManager.IMPORTANCE_HIGH,
            )
            channel.description = "Срабатывания распознавания и события камер"
            manager.createNotificationChannel(channel)
        }
    }

    override fun getName(): String = "NvrPushNotify"

    /**
     * Показывает уведомление о событии.
     *
     * tag — идентификатор события с сервера: разные тревоги не затирают друг
     * друга, и по одному событию не появляется второе уведомление.
     */
    @ReactMethod
    fun notify(title: String, text: String, tag: String) {
        val context = reactApplicationContext
        try {
            createChannel(context)

            // Нажатие возвращает в приложение: оно уже запущено, и новый
            // экран открывать не нужно — приложение само перейдёт по событию.
            val launchIntent = context.packageManager.getLaunchIntentForPackage(context.packageName)
            val pendingIntent = launchIntent?.let {
                PendingIntent.getActivity(
                    context,
                    0,
                    it.apply {
                        addFlags(
                            Intent.FLAG_ACTIVITY_NEW_TASK or
                                Intent.FLAG_ACTIVITY_SINGLE_TOP,
                        )
                    },
                    PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
                )
            }

            val builder = NotificationCompat.Builder(context, CHANNEL_ID)
                .setContentTitle(title)
                .setContentText(text)
                .setSmallIcon(android.R.drawable.ic_dialog_alert)
                .setPriority(NotificationCompat.PRIORITY_HIGH)
                .setCategory(NotificationCompat.CATEGORY_ALARM)
                .setAutoCancel(true)

            if (pendingIntent != null) {
                builder.setContentIntent(pendingIntent)
            }

            val manager =
                context.getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager
            manager.notify(tag, tag.hashCode(), builder.build())
        } catch (error: Exception) {
            // Уведомление — не то, ради чего стоит ронять приложение: само
            // событие уже пришло, и экран его покажет. Молча выходим.
        }
    }
}
