<?php

namespace App\Models;

use Illuminate\Database\Eloquent\Concerns\HasUuids;
use Illuminate\Database\Eloquent\Model;
use Illuminate\Database\Eloquent\Relations\BelongsToMany;
use Illuminate\Database\Eloquent\Relations\HasMany;

/**
 * Class Product
 *
 * @property string $id
 * @property string $name
 * @property string $description
 * @property float $price
 * @property string $created_at
 * @property string $updated_at
 * @property bool $is_active
 * @property Attachment[] $attachments
 * @property ProductTag[] $productTags
 * @property ProductTranslation[] $productTranslations
 * @property Order[] $orders
 */
class Product extends Model
{
    use HasUuids;

    protected $table = 'products';

    protected $primaryKey = 'id';

    public $incrementing = false;

    protected $keyType = 'string';

    protected $fillable = [
        'id',
        'name',
        'description',
        'price',
        'created_at',
        'updated_at',
        'is_active',
    ];

    public function attachments(): HasMany
    {
        return $this->hasMany(Attachment::class);
    }

    public function productTags(): BelongsToMany
    {
        return $this->belongsToMany(ProductTag::class);
    }

    public function productTranslations(): HasMany
    {
        return $this->hasMany(ProductTranslation::class);
    }

    public function orders(): BelongsToMany
    {
        return $this->belongsToMany(Order::class)->withPivot('quantity');
    }
}
