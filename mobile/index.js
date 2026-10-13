/**
 * @format
 */

import {AppRegistry} from 'react-native';
import App from './App';
import {name as appName} from './app.json';
import {installCrashGuard} from './src/net/crashGuard';
import {installBackgroundPush} from './src/net/pushBackground';

// Перехват ставим до запуска приложения: ошибка при старте тоже должна быть
// видна текстом, а не выглядеть как «приложение не открывается».
installCrashGuard();

// Обработчик фоновых push — до интерфейса: сообщение о входящем звонке может
// прийти и тогда, когда интерфейса нет вовсе (приложение выгружено).
installBackgroundPush();

AppRegistry.registerComponent(appName, () => App);
