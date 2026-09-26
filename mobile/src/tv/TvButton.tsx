import React, { useState } from 'react';
import {
  Pressable,
  StyleSheet,
  Text,
  type StyleProp,
  type ViewStyle,
} from 'react-native';
import { colors, radius, spacing } from '../theme';

/**
 * Кнопка, управляемая пультом.
 *
 * Касание и фокус — разные вещи. На телефоне достаточно Pressable, а на
 * телевизоре пользователь перемещает фокус стрелками, и до нажатия «ОК»
 * элемент должен быть заметно выделен: иначе непонятно, что сработает.
 *
 * Компонент сам следит за фокусом через onFocus/onBlur: React Native не
 * даёт стилизовать фокус декларативно, как CSS-псевдокласс :focus.
 */

export default function TvButton({
  title,
  onPress,
  disabled = false,
  variant = 'default',
  hasTVPreferredFocus = false,
  style,
}: {
  title: string;
  onPress: () => void;
  disabled?: boolean;
  /** primary — основное действие, danger — удаление. */
  variant?: 'default' | 'primary' | 'danger';
  /**
   * Куда поставить фокус при появлении экрана. Должна быть выставлена
   * ровно у одной кнопки: при нескольких флагах поведение зависит от
   * порядка обхода, и фокус может оказаться не там, где ожидается.
   */
  hasTVPreferredFocus?: boolean;
  style?: StyleProp<ViewStyle>;
}) {
  const [focused, setFocused] = useState(false);

  return (
    <Pressable
      onPress={onPress}
      disabled={disabled}
      onFocus={() => setFocused(true)}
      onBlur={() => setFocused(false)}
      hasTVPreferredFocus={hasTVPreferredFocus}
      style={[
        styles.base,
        variantStyles[variant],
        focused && styles.focused,
        disabled && styles.disabled,
        style,
      ]}
    >
      <Text
        style={[
          styles.text,
          variant === 'primary' && styles.textPrimary,
          focused && styles.textFocused,
          disabled && styles.textDisabled,
        ]}
      >
        {title}
      </Text>
    </Pressable>
  );
}

const styles = StyleSheet.create({
  base: {
    backgroundColor: colors.surface,
    borderWidth: 2,
    borderColor: colors.border,
    borderRadius: radius.md,
    // Отступы крупные: на телевизоре элементы видно с расстояния,
    // и мелкая кнопка требует прицеливания пультом.
    paddingHorizontal: spacing.xl,
    paddingVertical: spacing.md,
    alignItems: 'center',
    justifyContent: 'center',
  },
  focused: {
    borderColor: colors.primary,
    backgroundColor: colors.surfaceAlt,
    // Толщина границы не меняется при фокусе: иначе соседние элементы
    // сдвигались бы, и список «дёргался» при навигации.
    transform: [{ scale: 1.04 }],
  },
  disabled: { opacity: 0.45 },
  text: { color: colors.text, fontSize: 16, fontWeight: '600' },
  textPrimary: { color: '#fff' },
  textFocused: { color: '#fff' },
  textDisabled: { color: colors.textMuted },
});

const variantStyles: Record<string, ViewStyle> = {
  default: {},
  primary: { backgroundColor: colors.primary, borderColor: colors.primary },
  danger: { borderColor: colors.danger },
};
